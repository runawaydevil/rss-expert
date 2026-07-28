package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

const (
	MinPollInterval          = 15 * time.Minute
	MaxPollInterval          = 24 * time.Hour
	DefaultPollInterval      = 15 * time.Minute
	MaxFailuresBeforeBackoff = 3
)

const (
	ProtocolFeed        = "feed"
	ProtocolActivityPub = "activitypub"
)

var (
	ErrSourceIsLocal   = errors.New("ingest: that is this instance's own feed, not a subscription")
	ErrNotSubscribed   = errors.New("ingest: that source is provenance, not a subscription")
	ErrNoSource        = errors.New("no such source")
	ErrSourceExists    = errors.New("this instance already follows that feed")
	ErrFeedURLUnusable = errors.New("that is not a usable feed address")
)

type Source struct {
	ID           int64
	FeedURL      string
	SiteURL      string
	Title        string
	SelfURL      string
	ETag         string
	LastModified string
	LastFetchAt  time.Time
	LastStatus   int
	LastError    string
	FailureCount int
	PollInterval time.Duration
	NextPollAt   time.Time
	Quarantined  bool
	Local        bool
	Protocol     string
}

func (s *Source) Subscribed() bool { return !s.Local && s.Protocol == ProtocolFeed }

func (s *Source) Host() string {
	u, err := url.Parse(s.FeedURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

type Store struct {
	db *store.DB
}

func NewStore(db *store.DB) *Store { return &Store{db: db} }

func normaliseFeedURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrFeedURLUnusable
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrFeedURLUnusable
	}
	if u.Hostname() == "" {
		return "", ErrFeedURLUnusable
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String(), nil
}

func (s *Store) AddSource(ctx context.Context, feedURL string) (*Source, error) {
	normalised, err := normaliseFeedURL(feedURL)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	res, err := s.db.Write.ExecContext(ctx,
		`insert into source (feed_url, poll_interval, next_poll_at, created_at)
		 values (?, ?, ?, ?)`,
		normalised, int64(DefaultPollInterval.Seconds()), now.Unix(), now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: source.feed_url") {
			return s.SourceByURL(ctx, normalised)
		}
		return nil, fmt.Errorf("ingest: add source: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.SourceByID(ctx, id)
}

const sourceColumns = `id, feed_url, coalesce(site_url, ''), coalesce(title, ''),
	coalesce(self_url, ''), coalesce(etag, ''), coalesce(last_modified, ''),
	coalesce(last_fetch_at, 0), coalesce(last_status, 0), coalesce(last_error, ''),
	failure_count, poll_interval, next_poll_at, quarantined_at is not null, is_local,
	protocol`

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var (
		s            Source
		lastFetch    int64
		nextPoll     int64
		pollInterval int64
	)
	err := row.Scan(&s.ID, &s.FeedURL, &s.SiteURL, &s.Title, &s.SelfURL, &s.ETag,
		&s.LastModified, &lastFetch, &s.LastStatus, &s.LastError, &s.FailureCount,
		&pollInterval, &nextPoll, &s.Quarantined, &s.Local, &s.Protocol)
	if err != nil {
		return nil, err
	}
	if lastFetch > 0 {
		s.LastFetchAt = time.Unix(lastFetch, 0).UTC()
	}
	s.PollInterval = time.Duration(pollInterval) * time.Second
	s.NextPollAt = time.Unix(nextPoll, 0).UTC()
	return &s, nil
}

func (s *Store) SourceByID(ctx context.Context, id int64) (*Source, error) {
	row := s.db.Read.QueryRowContext(ctx, `select `+sourceColumns+` from source where id = ?`, id)
	source, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSource
	}
	return source, err
}

func (s *Store) SourceByURL(ctx context.Context, feedURL string) (*Source, error) {
	row := s.db.Read.QueryRowContext(ctx, `select `+sourceColumns+` from source where feed_url = ?`, feedURL)
	source, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSource
	}
	return source, err
}

func (s *Store) Sources(ctx context.Context) ([]*Source, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+sourceColumns+` from source
		 where is_local = 0 and protocol = ? order by id`, ProtocolFeed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]*Source, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+sourceColumns+` from source
		 where quarantined_at is null and is_local = 0 and protocol = ?
		   and next_poll_at <= ?
		 order by next_poll_at limit ?`, ProtocolFeed, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

type FetchOutcome struct {
	Status       int
	ETag         string
	LastModified string
	Err          error
	Cadence      time.Duration
}

func (s *Store) RecordFetch(ctx context.Context, sourceID int64, out FetchOutcome, now time.Time) error {
	source, err := s.SourceByID(ctx, sourceID)
	if err != nil {
		return err
	}

	failures := source.FailureCount
	message := ""
	if out.Err != nil {
		failures++
		message = out.Err.Error()
	} else if out.Status >= 400 {
		failures++
		message = fmt.Sprintf("HTTP %d", out.Status)
	} else {
		failures = 0
	}

	interval := nextInterval(source.PollInterval, out.Cadence, failures)

	_, err = s.db.Write.ExecContext(ctx,
		`update source set
		   etag = ?, last_modified = ?, last_fetch_at = ?, last_status = ?,
		   last_error = ?, failure_count = ?, poll_interval = ?, next_poll_at = ?
		 where id = ?`,
		nullable(out.ETag), nullable(out.LastModified), now.Unix(), out.Status,
		nullable(message), failures, int64(interval.Seconds()),
		now.Add(interval).Unix(), sourceID)
	return err
}

func nextInterval(current, cadence time.Duration, failures int) time.Duration {
	interval := current
	if cadence > 0 {
		interval = cadence / 2
	}
	if failures >= MaxFailuresBeforeBackoff {
		interval = current * 2
	}
	return clampInterval(interval)
}

func clampInterval(d time.Duration) time.Duration {
	if d < MinPollInterval {
		return MinPollInterval
	}
	if d > MaxPollInterval {
		return MaxPollInterval
	}
	return d
}

func (s *Store) DescribeFeed(ctx context.Context, sourceID int64, title, siteURL, selfURL string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update source set title = ?, site_url = ?, self_url = ? where id = ?`,
		nullable(title), nullable(siteURL), nullable(selfURL), sourceID)
	return err
}

func (s *Store) Quarantine(ctx context.Context, sourceID int64, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update source set quarantined_at = ? where id = ?`, now.Unix(), sourceID)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) RemoveSource(ctx context.Context, id int64) error {
	source, err := s.SourceByID(ctx, id)
	if err != nil {
		return err
	}
	if source.Local {
		return ErrSourceIsLocal
	}

	orphaned, err := s.itemsWinningOn(ctx, id)
	if err != nil {
		return err
	}

	if _, err := s.db.Write.ExecContext(ctx,
		`delete from logical_item where winner_id in
		 (select id from observation where source_id = ?)`, id); err != nil {
		return fmt.Errorf("ingest: detach items: %w", err)
	}
	if _, err := s.db.Write.ExecContext(ctx, `delete from source where id = ?`, id); err != nil {
		return fmt.Errorf("ingest: remove source: %w", err)
	}

	now := time.Now().UTC()
	for _, key := range orphaned {
		if _, err := s.Converge(ctx, key, now); err != nil {
			return fmt.Errorf("ingest: reconverge %s: %w", key, err)
		}
		if err := s.forget(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) itemsWinningOn(ctx context.Context, sourceID int64) ([]string, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select l.item_key from logical_item l
		 join observation o on o.id = l.winner_id
		 where o.source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) forget(ctx context.Context, key string) error {
	var exists int
	err := s.db.Read.QueryRowContext(ctx,
		`select count(*) from logical_item where item_key = ?`, key).Scan(&exists)
	if err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	if _, err := s.db.Write.ExecContext(ctx,
		`delete from item_search where item_key = ?`, key); err != nil {
		return fmt.Errorf("ingest: forget %s: %w", key, err)
	}
	return nil
}

func (s *Store) MarkPushed(ctx context.Context, sourceID int64, at time.Time) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update source set last_push_at = ?, last_fetch_at = ?, failure_count = 0, last_error = null
		 where id = ?`, at.Unix(), at.Unix(), sourceID)
	return err
}
