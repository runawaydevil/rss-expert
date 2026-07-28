package moderation

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

type Kind string

const (
	Domain Kind = "domain"
	Source Kind = "source"
	Item   Kind = "item"
	Word   Kind = "word"
)

var (
	ErrNotAllowed = errors.New("that is not yours to moderate")
	ErrNoReport   = errors.New("no such report")
	ErrEmptyValue = errors.New("there is nothing to block")
)

type Block struct {
	ID        int64
	AccountID int64
	Instance  bool
	Kind      Kind
	Value     string
	Reason    string
	CreatedAt time.Time
}

type Report struct {
	ID        int64
	ItemKey   string
	Reporter  int64
	Reason    string
	Context   string
	State     string
	Note      string
	CreatedAt time.Time
}

type Store struct {
	db *store.DB
}

func New(db *store.DB) *Store { return &Store{db: db} }

func normalise(kind Kind, value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "", ErrEmptyValue
	}

	switch kind {
	case Domain:
		if u, err := url.Parse(value); err == nil && u.Hostname() != "" {
			value = u.Hostname()
		}
		return strings.TrimPrefix(value, "www."), nil
	case Word:
		return value, nil
	case Source, Item:
		return value, nil
	}
	return "", fmt.Errorf("moderation: unknown block kind %q", kind)
}

func (s *Store) Block(ctx context.Context, actor *identity.Account, forAccount int64, kind Kind, value, reason string) error {
	normalised, err := normalise(kind, value)
	if err != nil {
		return err
	}

	instanceWide := forAccount == 0
	if instanceWide && !actor.Role.CanModerate() {
		return ErrNotAllowed
	}
	if !instanceWide && forAccount != actor.ID && !actor.Role.CanModerate() {
		return ErrNotAllowed
	}

	var owner any
	if !instanceWide {
		owner = forAccount
	}

	_, err = s.db.Write.ExecContext(ctx,
		`insert into block (account_id, kind, value, reason, created_by, created_at)
		 values (?, ?, ?, ?, ?, ?) on conflict do nothing`,
		owner, string(kind), normalised, nullable(reason), actor.ID, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("moderation: block: %w", err)
	}

	scope := "instance"
	if !instanceWide {
		scope = fmt.Sprintf("account %d", forAccount)
	}
	return s.Log(ctx, actor, "block."+string(kind), normalised, scope)
}

func (s *Store) Unblock(ctx context.Context, actor *identity.Account, forAccount int64, kind Kind, value string) error {
	normalised, err := normalise(kind, value)
	if err != nil {
		return err
	}
	if forAccount == 0 && !actor.Role.CanModerate() {
		return ErrNotAllowed
	}

	var owner any
	if forAccount != 0 {
		owner = forAccount
	}
	if _, err := s.db.Write.ExecContext(ctx,
		`delete from block where coalesce(account_id, 0) = coalesce(?, 0) and kind = ? and value = ?`,
		owner, string(kind), normalised); err != nil {
		return err
	}
	return s.Log(ctx, actor, "unblock."+string(kind), normalised, "")
}

type Filter struct {
	domains map[string]bool
	sources map[string]bool
	items   map[string]bool
	words   []string
}

func (f *Filter) Hides(itemKey, link, feedURL, text string) (bool, string) {
	if f.items[strings.ToLower(itemKey)] {
		return true, "this item is blocked"
	}
	if f.sources[strings.ToLower(feedURL)] {
		return true, "this source is blocked"
	}

	for _, candidate := range []string{link, itemKey, feedURL} {
		if host := hostOf(candidate); host != "" && f.domains[host] {
			return true, host + " is blocked"
		}
	}

	lowered := strings.ToLower(text)
	for _, word := range f.words {
		if strings.Contains(lowered, word) {
			return true, "contains a muted word"
		}
	}
	return false, ""
}

func (s *Store) FilterFor(ctx context.Context, accountID int64) (*Filter, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select kind, value from block where account_id is null or account_id = ?`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	f := &Filter{
		domains: map[string]bool{},
		sources: map[string]bool{},
		items:   map[string]bool{},
	}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, err
		}
		switch Kind(kind) {
		case Domain:
			f.domains[value] = true
		case Source:
			f.sources[value] = true
		case Item:
			f.items[value] = true
		case Word:
			f.words = append(f.words, value)
		}
	}
	return f, rows.Err()
}

func (s *Store) Blocks(ctx context.Context, accountID int64) ([]Block, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, coalesce(account_id, 0), kind, value, coalesce(reason, ''), created_at
		 from block where account_id is null or account_id = ? order by created_at desc`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Block
	for rows.Next() {
		var (
			b       Block
			kind    string
			created int64
		)
		if err := rows.Scan(&b.ID, &b.AccountID, &kind, &b.Value, &b.Reason, &created); err != nil {
			return nil, err
		}
		b.Kind = Kind(kind)
		b.Instance = b.AccountID == 0
		b.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Report(ctx context.Context, reporter *identity.Account, itemKey, reason, context_ string) (int64, error) {
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("moderation: a report needs a reason")
	}
	var who any
	if reporter != nil {
		who = reporter.ID
	}
	res, err := s.db.Write.ExecContext(ctx,
		`insert into report (item_key, reporter, reason, context, created_at) values (?, ?, ?, ?, ?)`,
		itemKey, who, reason, nullable(context_), time.Now().UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("moderation: report: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) OpenReports(ctx context.Context, limit int) ([]Report, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, item_key, coalesce(reporter, 0), reason, coalesce(context, ''),
		        state, coalesce(note, ''), created_at
		 from report where state = 'open' order by created_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Report
	for rows.Next() {
		var (
			r       Report
			created int64
		)
		if err := rows.Scan(&r.ID, &r.ItemKey, &r.Reporter, &r.Reason, &r.Context,
			&r.State, &r.Note, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Decide(ctx context.Context, actor *identity.Account, id int64, upheld bool, note string) error {
	if !actor.Role.CanModerate() {
		return ErrNotAllowed
	}

	state := "dismissed"
	if upheld {
		state = "upheld"
	}
	res, err := s.db.Write.ExecContext(ctx,
		`update report set state = ?, decided_by = ?, decided_at = ?, note = ?
		 where id = ? and state = 'open'`,
		state, actor.ID, time.Now().UTC().Unix(), nullable(note), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoReport
	}
	return s.Log(ctx, actor, "report."+state, fmt.Sprint(id), note)
}

func (s *Store) Log(ctx context.Context, actor *identity.Account, action, subject, detail string) error {
	var who any
	role := "system"
	if actor != nil {
		who = actor.ID
		role = string(actor.Role)
	}
	_, err := s.db.Write.ExecContext(ctx,
		`insert into audit (actor, actor_role, action, subject, detail, at) values (?, ?, ?, ?, ?, ?)`,
		who, role, action, nullable(subject), nullable(detail), time.Now().UTC().Unix())
	return err
}

type Entry struct {
	ID      int64
	Actor   int64
	Role    string
	Action  string
	Subject string
	Detail  string
	At      time.Time
}

func (s *Store) Audit(ctx context.Context, limit int) ([]Entry, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, coalesce(actor, 0), actor_role, action, coalesce(subject, ''),
		        coalesce(detail, ''), at
		 from audit order by at desc, id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var (
			e  Entry
			at int64
		)
		if err := rows.Scan(&e.ID, &e.Actor, &e.Role, &e.Action, &e.Subject, &e.Detail, &at); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
