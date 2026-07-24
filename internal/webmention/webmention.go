package webmention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/indieweb"
	"github.com/runawaydevil/rss-expert/internal/safety"
	"github.com/runawaydevil/rss-expert/internal/store"
)

type Direction string

const (
	Incoming Direction = "in"
	Outgoing Direction = "out"
)

type State string

const (
	Pending  State = "pending"
	Verified State = "verified"
	Approved State = "approved"
	Rejected State = "rejected"
	Failed   State = "failed"
	Deleted  State = "deleted"
)

var (
	ErrSameURL       = errors.New("source and target cannot be the same page")
	ErrTargetNotOurs = errors.New("that target is not on this instance")
	ErrNoSuchTarget  = errors.New("that target does not exist here")
	ErrNoLink        = errors.New("the source page does not link to the target")
	ErrUnreachable   = errors.New("the source page could not be read")
	ErrNotFound      = errors.New("no such webmention")
	ErrNoEndpoint    = errors.New("that site advertises no webmention endpoint")
	ErrBadURL        = errors.New("that is not a usable address")
)

type Mention struct {
	ID          int64
	Direction   Direction
	Source      string
	Target      string
	State       State
	AuthorName  string
	AuthorURL   string
	AuthorPhoto string
	Content     string
	Endpoint    string
	LastError   string
	ReceivedAt  time.Time
	VerifiedAt  time.Time
	DecidedAt   time.Time
}

func (m *Mention) Settled() bool {
	return m.State == Approved || m.State == Rejected || m.State == Deleted
}

type Store struct {
	db      *store.DB
	fetcher *safety.Fetcher
	domain  string
}

type Options struct {
	Domain            string
	UserAgent         string
	AllowPrivateAddrs bool
}

func New(db *store.DB, o Options) *Store {
	return &Store{
		db:     db,
		domain: o.Domain,
		fetcher: safety.New(safety.Options{
			UserAgent:          o.UserAgent,
			AcceptContentTypes: []string{"text/html", "application/xhtml+xml"},
			AllowPrivateAddrs:  o.AllowPrivateAddrs,
			MaxBytes:           1 << 20,
		}),
	}
}

func usable(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, ErrBadURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrBadURL
	}
	return u, nil
}

func (s *Store) ownsTarget(target *url.URL) bool {
	ours := strings.ToLower(strings.TrimSpace(s.domain))
	if u, err := url.Parse(ours); err == nil && u.Host != "" {
		ours = u.Host
	}
	ours = strings.TrimPrefix(ours, "www.")

	host := strings.TrimPrefix(strings.ToLower(target.Host), "www.")
	if host == ours {
		return true
	}
	return strings.TrimPrefix(strings.ToLower(target.Hostname()), "www.") == ours
}

func (s *Store) Receive(ctx context.Context, rawSource, rawTarget string) (*Mention, error) {
	source, err := usable(rawSource)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	target, err := usable(rawTarget)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if source.String() == target.String() {
		return nil, ErrSameURL
	}
	if !s.ownsTarget(target) {
		return nil, ErrTargetNotOurs
	}

	now := time.Now().UTC()
	_, err = s.db.Write.ExecContext(ctx,
		`insert into webmention (direction, source, target, state, received_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (direction, source, target) do update set
		   state = ?, received_at = ?, last_error = null`,
		string(Incoming), source.String(), target.String(), string(Pending), now.Unix(),
		string(Pending), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("webmention: record: %w", err)
	}
	return s.find(ctx, Incoming, source.String(), target.String())
}

func (s *Store) VerifyIncoming(ctx context.Context, m *Mention) error {
	now := time.Now().UTC()

	result, err := s.fetcher.Get(ctx, m.Source, http.Header{})
	if err != nil {
		return s.fail(ctx, m.ID, fmt.Errorf("%w: %v", ErrUnreachable, err))
	}

	if result.StatusCode == http.StatusGone || result.StatusCode == http.StatusNotFound {
		_, err := s.db.Write.ExecContext(ctx,
			`update webmention set state = ?, verified_at = ?, last_error = null where id = ?`,
			string(Deleted), now.Unix(), m.ID)
		return err
	}
	if result.StatusCode >= 400 {
		return s.fail(ctx, m.ID, fmt.Errorf("%w: HTTP %d", ErrUnreachable, result.StatusCode))
	}

	body := string(result.Body)
	if !strings.Contains(body, m.Target) {
		return s.fail(ctx, m.ID, ErrNoLink)
	}

	page, err := indieweb.Discover(result.URL, result.Body)
	if err != nil {
		page = &indieweb.Page{}
	}

	authorURL := ""
	if len(page.Card.URLs) > 0 {
		authorURL = page.Card.URLs[0]
	}

	_, err = s.db.Write.ExecContext(ctx,
		`update webmention set state = ?, verified_at = ?, last_error = null,
		                        author_name = ?, author_url = ?, author_photo = ?
		 where id = ?`,
		string(Verified), now.Unix(),
		nullable(page.Card.Name), nullable(authorURL), nullable(page.Card.Photo), m.ID)
	return err
}

func (s *Store) fail(ctx context.Context, id int64, cause error) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update webmention set state = ?, last_error = ? where id = ?`,
		string(Failed), cause.Error(), id)
	if err != nil {
		return err
	}
	return cause
}

func (s *Store) Decide(ctx context.Context, id, moderator int64, approve bool) error {
	state := Rejected
	if approve {
		state = Approved
	}
	res, err := s.db.Write.ExecContext(ctx,
		`update webmention set state = ?, decided_at = ?, decided_by = ?
		 where id = ? and direction = ?`,
		string(state), time.Now().UTC().Unix(), moderator, id, string(Incoming))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DiscoverEndpoint(ctx context.Context, rawTarget string) (string, error) {
	target, err := usable(rawTarget)
	if err != nil {
		return "", err
	}

	result, err := s.fetcher.Get(ctx, target.String(), http.Header{})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if result.StatusCode >= 400 {
		return "", fmt.Errorf("%w: HTTP %d", ErrUnreachable, result.StatusCode)
	}

	if endpoint := endpointFromLinkHeader(result.Header.Values("Link"), result.URL); endpoint != "" {
		return endpoint, nil
	}

	page, err := indieweb.Discover(result.URL, result.Body)
	if err != nil {
		return "", ErrNoEndpoint
	}
	if page.Webmention == "" {
		return "", ErrNoEndpoint
	}
	return page.Webmention, nil
}

func endpointFromLinkHeader(headers []string, base *url.URL) string {
	for _, header := range headers {
		for _, part := range strings.Split(header, ",") {
			segments := strings.Split(part, ";")
			if len(segments) < 2 {
				continue
			}

			href := strings.Trim(strings.TrimSpace(segments[0]), "<>")
			isWebmention := false
			for _, segment := range segments[1:] {
				segment = strings.ToLower(strings.TrimSpace(segment))
				if !strings.HasPrefix(segment, "rel=") {
					continue
				}
				value := strings.Trim(strings.TrimPrefix(segment, "rel="), `"'`)
				for _, rel := range strings.Fields(value) {
					if rel == "webmention" {
						isWebmention = true
					}
				}
			}
			if !isWebmention {
				continue
			}

			parsed, err := url.Parse(href)
			if err != nil {
				continue
			}
			if base != nil {
				parsed = base.ResolveReference(parsed)
			}
			if parsed.Scheme == "http" || parsed.Scheme == "https" {
				return parsed.String()
			}
		}
	}
	return ""
}

func (s *Store) Send(ctx context.Context, source, target string) (*Mention, error) {
	now := time.Now().UTC()

	if _, err := s.db.Write.ExecContext(ctx,
		`insert into webmention (direction, source, target, state, received_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (direction, source, target) do update set state = ?, last_error = null`,
		string(Outgoing), source, target, string(Pending), now.Unix(), string(Pending)); err != nil {
		return nil, err
	}

	mention, err := s.find(ctx, Outgoing, source, target)
	if err != nil {
		return nil, err
	}

	endpoint, err := s.DiscoverEndpoint(ctx, target)
	if err != nil {
		s.fail(ctx, mention.ID, err)
		return mention, err
	}

	form := url.Values{"source": {source}, "target": {target}}
	status, err := s.post(ctx, endpoint, form)
	if err != nil {
		s.fail(ctx, mention.ID, err)
		return mention, err
	}
	if status >= 400 {
		failure := fmt.Errorf("the endpoint answered HTTP %d", status)
		s.fail(ctx, mention.ID, failure)
		return mention, failure
	}

	if _, err := s.db.Write.ExecContext(ctx,
		`update webmention set state = ?, endpoint = ?, verified_at = ?, last_error = null where id = ?`,
		string(Approved), endpoint, now.Unix(), mention.ID); err != nil {
		return mention, err
	}
	mention.State = Approved
	mention.Endpoint = endpoint
	return mention, nil
}

func (s *Store) post(ctx context.Context, endpoint string, form url.Values) (int, error) {
	if _, err := usable(endpoint); err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.fetcher.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

const mentionColumns = `id, direction, source, target, state,
	coalesce(author_name, ''), coalesce(author_url, ''), coalesce(author_photo, ''),
	coalesce(content, ''), coalesce(endpoint, ''), coalesce(last_error, ''),
	received_at, coalesce(verified_at, 0), coalesce(decided_at, 0)`

func scan(row interface{ Scan(...any) error }) (*Mention, error) {
	var (
		m                           Mention
		direction, state            string
		received, verified, decided int64
	)
	err := row.Scan(&m.ID, &direction, &m.Source, &m.Target, &state,
		&m.AuthorName, &m.AuthorURL, &m.AuthorPhoto, &m.Content, &m.Endpoint,
		&m.LastError, &received, &verified, &decided)
	if err != nil {
		return nil, err
	}
	m.Direction = Direction(direction)
	m.State = State(state)
	m.ReceivedAt = time.Unix(received, 0).UTC()
	if verified > 0 {
		m.VerifiedAt = time.Unix(verified, 0).UTC()
	}
	if decided > 0 {
		m.DecidedAt = time.Unix(decided, 0).UTC()
	}
	return &m, nil
}

func (s *Store) find(ctx context.Context, direction Direction, source, target string) (*Mention, error) {
	row := s.db.Read.QueryRowContext(ctx,
		`select `+mentionColumns+` from webmention
		 where direction = ? and source = ? and target = ?`,
		string(direction), source, target)
	mention, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return mention, err
}

func (s *Store) ByID(ctx context.Context, id int64) (*Mention, error) {
	row := s.db.Read.QueryRowContext(ctx, `select `+mentionColumns+` from webmention where id = ?`, id)
	mention, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return mention, err
}

func (s *Store) collect(ctx context.Context, query string, args ...any) ([]*Mention, error) {
	rows, err := s.db.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Mention
	for rows.Next() {
		mention, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mention)
	}
	return out, rows.Err()
}

func (s *Store) AwaitingModeration(ctx context.Context, limit int) ([]*Mention, error) {
	return s.collect(ctx,
		`select `+mentionColumns+` from webmention
		 where direction = 'in' and state = 'verified'
		 order by received_at desc limit ?`, limit)
}

func (s *Store) Approved(ctx context.Context, target string) ([]*Mention, error) {
	return s.collect(ctx,
		`select `+mentionColumns+` from webmention
		 where direction = 'in' and state = 'approved' and target = ?
		 order by received_at`, target)
}

func (s *Store) Journey(ctx context.Context, source string) ([]*Mention, error) {
	return s.collect(ctx,
		`select `+mentionColumns+` from webmention
		 where direction = 'out' and source = ? order by received_at`, source)
}

func (s *Store) Unverified(ctx context.Context, limit int) ([]*Mention, error) {
	return s.collect(ctx,
		`select `+mentionColumns+` from webmention
		 where direction = 'in' and state = 'pending'
		 order by received_at limit ?`, limit)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
