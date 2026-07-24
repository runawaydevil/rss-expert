package indieweb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/safety"
	"github.com/runawaydevil/rss-expert/internal/store"
)

var (
	ErrNoSite      = errors.New("no such site")
	ErrSiteClaimed = errors.New("someone has already claimed that domain")
	ErrBadURL      = errors.New("that is not a usable site address")
	ErrNotYours    = errors.New("that site belongs to another account")
	ErrNoBackLink  = errors.New("that site does not link back here")
	ErrUnreachable = errors.New("that site could not be read")
)

type State string

const (
	Claimed  State = "claimed"
	Verified State = "verified"
	Failing  State = "failing"
)

type Site struct {
	ID         int64
	AccountID  int64
	URL        string
	Host       string
	Name       string
	Photo      string
	Note       string
	FeedURL    string
	VerifiedAt time.Time
	CheckedAt  time.Time
	LastError  string
	CreatedAt  time.Time
}

func (s *Site) State() State {
	switch {
	case !s.VerifiedAt.IsZero():
		return Verified
	case s.LastError != "":
		return Failing
	}
	return Claimed
}

func (s *Site) Verified() bool { return !s.VerifiedAt.IsZero() }

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

func NewStore(db *store.DB, o Options) *Store {
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

func normaliseSite(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", ErrBadURL
	}
	if !strings.Contains(raw, "//") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", "", ErrBadURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", ErrBadURL
	}

	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawQuery = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), strings.TrimPrefix(u.Hostname(), "www."), nil
}

func (s *Store) ProfileURL(handle string) string {
	base := strings.TrimSuffix(s.domain, "/")
	if !strings.Contains(base, "//") {
		base = "https://" + base
	}
	return base + "/users/" + handle
}

func (s *Store) Claim(ctx context.Context, accountID int64, raw string) (*Site, error) {
	normalised, host, err := normaliseSite(raw)
	if err != nil {
		return nil, err
	}

	existing, err := s.ByHost(ctx, host)
	if err == nil {
		if existing.AccountID != accountID {
			return nil, ErrSiteClaimed
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNoSite) {
		return nil, err
	}

	res, err := s.db.Write.ExecContext(ctx,
		`insert into site (account_id, url, host, created_at) values (?, ?, ?, ?)`,
		accountID, normalised, host, time.Now().UTC().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrSiteClaimed
		}
		return nil, fmt.Errorf("indieweb: claim site: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

func (s *Store) Release(ctx context.Context, accountID, siteID int64) error {
	site, err := s.ByID(ctx, siteID)
	if err != nil {
		return err
	}
	if site.AccountID != accountID {
		return ErrNotYours
	}
	_, err = s.db.Write.ExecContext(ctx, `delete from site where id = ?`, siteID)
	return err
}

func (s *Store) Verify(ctx context.Context, site *Site, handle string) error {
	profile := s.ProfileURL(handle)
	now := time.Now().UTC()

	result, err := s.fetcher.Get(ctx, site.URL, http.Header{})
	if err != nil {
		s.recordCheck(ctx, site.ID, now, fmt.Errorf("%w: %v", ErrUnreachable, err))
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if result.StatusCode >= 400 {
		failure := fmt.Errorf("%w: HTTP %d", ErrUnreachable, result.StatusCode)
		s.recordCheck(ctx, site.ID, now, failure)
		return failure
	}

	page, err := Discover(result.URL, result.Body)
	if err != nil {
		s.recordCheck(ctx, site.ID, now, err)
		return err
	}

	var linksBack bool
	for _, href := range page.RelMe {
		if SameURL(href, profile) {
			linksBack = true
			break
		}
	}
	if !linksBack {
		s.recordCheck(ctx, site.ID, now, ErrNoBackLink)
		return ErrNoBackLink
	}

	feedURL := ""
	if len(page.Feeds) > 0 {
		feedURL = page.Feeds[0].URL
	}

	_, err = s.db.Write.ExecContext(ctx,
		`update site set verified_at = ?, checked_at = ?, last_error = null,
		                 name = ?, photo = ?, note = ?, feed_url = ?
		 where id = ?`,
		now.Unix(), now.Unix(),
		nullable(page.Card.Name), nullable(page.Card.Photo), nullable(page.Card.Note),
		nullable(feedURL), site.ID)
	return err
}

func (s *Store) recordCheck(ctx context.Context, siteID int64, at time.Time, cause error) {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	s.db.Write.ExecContext(ctx,
		`update site set checked_at = ?, last_error = ?, verified_at = null where id = ?`,
		at.Unix(), nullable(message), siteID)
}

const siteColumns = `id, account_id, url, host, coalesce(name, ''), coalesce(photo, ''),
	coalesce(note, ''), coalesce(feed_url, ''), coalesce(verified_at, 0),
	coalesce(checked_at, 0), coalesce(last_error, ''), created_at`

func scanSite(row interface{ Scan(...any) error }) (*Site, error) {
	var (
		s                          Site
		verified, checked, created int64
	)
	err := row.Scan(&s.ID, &s.AccountID, &s.URL, &s.Host, &s.Name, &s.Photo,
		&s.Note, &s.FeedURL, &verified, &checked, &s.LastError, &created)
	if err != nil {
		return nil, err
	}
	if verified > 0 {
		s.VerifiedAt = time.Unix(verified, 0).UTC()
	}
	if checked > 0 {
		s.CheckedAt = time.Unix(checked, 0).UTC()
	}
	s.CreatedAt = time.Unix(created, 0).UTC()
	return &s, nil
}

func (s *Store) ByID(ctx context.Context, id int64) (*Site, error) {
	row := s.db.Read.QueryRowContext(ctx, `select `+siteColumns+` from site where id = ?`, id)
	site, err := scanSite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSite
	}
	return site, err
}

func (s *Store) ByHost(ctx context.Context, host string) (*Site, error) {
	row := s.db.Read.QueryRowContext(ctx, `select `+siteColumns+` from site where host = ?`,
		strings.TrimPrefix(strings.ToLower(host), "www."))
	site, err := scanSite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSite
	}
	return site, err
}

func (s *Store) ForAccount(ctx context.Context, accountID int64) ([]*Site, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+siteColumns+` from site where account_id = ? order by created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) VerifiedFor(ctx context.Context, accountID int64) (*Site, error) {
	sites, err := s.ForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, site := range sites {
		if site.Verified() {
			return site, nil
		}
	}
	return nil, ErrNoSite
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
