package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feed"
)

type Item struct {
	Key           string
	Title         string
	HTML          string
	Markdown      string
	Author        string
	Link          string
	Published     time.Time
	Updated       time.Time
	InReplyTo     string
	CommentsURL   string
	CommentsCount int
	OriginName    string
	OriginURL     string
	SourceID      int64
	SourceTitle   string
	SourceFeedURL string
	SourceSiteURL string
	Reason        string
	Enclosures    []feed.Enclosure
}

func (i *Item) Playable() []feed.Enclosure {
	var out []feed.Enclosure
	for _, e := range i.Enclosures {
		if strings.HasPrefix(e.Type, "audio/") || strings.HasPrefix(e.Type, "video/") {
			out = append(out, e)
		}
	}
	return out
}

func (i *Item) Host() string {
	for _, candidate := range []string{i.Link, i.Key, i.SourceSiteURL, i.SourceFeedURL} {
		if u, err := url.Parse(candidate); err == nil && u.Hostname() != "" {
			return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
	}
	return ""
}

func (i *Item) DisplayAuthor() string {
	for _, candidate := range []string{i.OriginName, i.Author, i.SourceTitle} {
		if candidate != "" {
			return candidate
		}
	}
	return i.Host()
}

func (i *Item) Edited() bool {
	return !i.Updated.IsZero() && !i.Published.IsZero() && i.Updated.After(i.Published)
}

const itemColumns = `
	l.item_key, l.reason,
	coalesce(o.title, ''), coalesce(o.html, ''), coalesce(o.markdown, ''),
	coalesce(o.author, ''), coalesce(o.link, ''),
	coalesce(o.published_at, 0), coalesce(o.updated_at, 0),
	coalesce(o.in_reply_to, ''), coalesce(o.comments_url, ''), coalesce(o.comments_count, 0),
	coalesce(o.origin_name, ''), coalesce(o.origin_url, ''),
	s.id, coalesce(s.title, ''), s.feed_url, coalesce(s.site_url, ''),
	coalesce(o.enclosures, '')`

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var (
		item               Item
		published, updated int64
		enclosures         string
	)
	err := row.Scan(&item.Key, &item.Reason, &item.Title, &item.HTML, &item.Markdown,
		&item.Author, &item.Link, &published, &updated, &item.InReplyTo,
		&item.CommentsURL, &item.CommentsCount, &item.OriginName, &item.OriginURL,
		&item.SourceID, &item.SourceTitle, &item.SourceFeedURL, &item.SourceSiteURL,
		&enclosures)
	if err != nil {
		return item, err
	}
	if enclosures != "" {
		if err := json.Unmarshal([]byte(enclosures), &item.Enclosures); err != nil {
			item.Enclosures = nil
		}
	}
	if published > 0 {
		item.Published = time.Unix(published, 0).UTC()
	}
	if updated > 0 {
		item.Updated = time.Unix(updated, 0).UTC()
	}
	return item, nil
}

func (s *Store) Timeline(ctx context.Context, limit int, before time.Time) ([]Item, error) {
	cutoff := int64(1<<62 - 1)
	if !before.IsZero() {
		cutoff = before.Unix()
	}

	rows, err := s.db.Read.QueryContext(ctx,
		`select `+itemColumns+`
		 from logical_item l
		 join observation o on o.id = l.winner_id
		 join source s on s.id = o.source_id
		 where coalesce(l.published_at, l.converged_at) < ?
		 order by coalesce(l.published_at, l.converged_at) desc
		 limit ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Item(ctx context.Context, key string) (Item, error) {
	row := s.db.Read.QueryRowContext(ctx,
		`select `+itemColumns+`
		 from logical_item l
		 join observation o on o.id = l.winner_id
		 join source s on s.id = o.source_id
		 where l.item_key = ?`, key)

	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return item, errors.New("ingest: no such item")
	}
	return item, err
}

func (s *Store) Replies(ctx context.Context, key string) ([]Item, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+itemColumns+`
		 from logical_item l
		 join observation o on o.id = l.winner_id
		 join source s on s.id = o.source_id
		 where l.in_reply_to = ?
		 order by coalesce(l.published_at, l.converged_at)`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Orphans(ctx context.Context) ([]string, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select distinct l.in_reply_to
		 from logical_item l
		 where l.in_reply_to is not null
		   and not exists (select 1 from logical_item p where p.item_key = l.in_reply_to)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			return nil, err
		}
		out = append(out, parent)
	}
	return out, rows.Err()
}

func (s *Store) Counts(ctx context.Context) (payloads, observations, items int, err error) {
	err = s.db.Read.QueryRowContext(ctx,
		`select (select count(*) from raw_payload),
		        (select count(*) from observation),
		        (select count(*) from logical_item)`).Scan(&payloads, &observations, &items)
	return
}

type Query struct {
	AccountID  int64
	Limit      int
	Before     time.Time
	UnreadOnly bool
	SavedOnly  bool
	SourceIDs  []int64
	Keys       []string
}

func (s *Store) Select(ctx context.Context, q Query) ([]Item, error) {
	if q.Limit <= 0 {
		q.Limit = 40
	}
	cutoff := int64(1<<62 - 1)
	if !q.Before.IsZero() {
		cutoff = q.Before.Unix()
	}

	where := []string{"coalesce(l.published_at, l.converged_at) < ?"}
	args := []any{q.AccountID, cutoff}

	if q.UnreadOnly {
		where = append(where, "r.read_at is null")
	}
	if q.SavedOnly {
		where = append(where, "r.saved_at is not null")
	}
	if len(q.SourceIDs) > 0 {
		where = append(where, "o.source_id in ("+list(len(q.SourceIDs))+")")
		for _, id := range q.SourceIDs {
			args = append(args, id)
		}
	}
	if len(q.Keys) > 0 {
		where = append(where, "l.item_key in ("+list(len(q.Keys))+")")
		for _, key := range q.Keys {
			args = append(args, key)
		}
	}

	order := "coalesce(l.published_at, l.converged_at) desc"
	if q.SavedOnly {
		order = "r.saved_at desc"
	}
	args = append(args, q.Limit)

	rows, err := s.db.Read.QueryContext(ctx,
		`select `+itemColumns+`
		 from logical_item l
		 join observation o on o.id = l.winner_id
		 join source s on s.id = o.source_id
		 left join read_state r on r.item_key = l.item_key and r.account_id = ?
		 where `+strings.Join(where, " and ")+`
		 order by `+order+` limit ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func list(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

type Health struct {
	SourceID  int64
	Title     string
	FeedURL   string
	State     string
	Detail    string
	Action    string
	LastFetch time.Time
}

func (s *Store) HealthFor(ctx context.Context, sourceIDs []int64) ([]Health, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		args = append(args, id)
	}

	rows, err := s.db.Read.QueryContext(ctx,
		`select s.id, coalesce(s.title, ''), s.feed_url, coalesce(s.last_error, ''),
		        s.failure_count, coalesce(s.last_fetch_at, 0), s.quarantined_at is not null,
		        (select max(coalesce(l.published_at, 0)) from logical_item l
		         join observation o on o.id = l.winner_id where o.source_id = s.id)
		 from source s where s.id in (`+list(len(sourceIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	var out []Health
	for rows.Next() {
		var (
			h           Health
			lastError   string
			failures    int
			lastFetch   int64
			quarantined bool
			newestPost  any
		)
		if err := rows.Scan(&h.SourceID, &h.Title, &h.FeedURL, &lastError,
			&failures, &lastFetch, &quarantined, &newestPost); err != nil {
			return nil, err
		}
		if lastFetch > 0 {
			h.LastFetch = time.Unix(lastFetch, 0).UTC()
		}

		switch {
		case quarantined:
			h.State, h.Detail, h.Action = "quarantined", "This source is held for review.", "Release"
		case failures >= 3:
			h.State = "failing"
			h.Detail = fmt.Sprintf("The last %d reads failed: %s", failures, lastError)
			h.Action = "Retry now"
		case lastError != "":
			h.State, h.Detail, h.Action = "warning", "The last read failed: "+lastError, "Retry now"
		default:
			h.State = "ok"
			if newest, ok := newestPost.(int64); ok && newest > 0 {
				quiet := now.Sub(time.Unix(newest, 0))
				if days := int(quiet.Hours() / 24); days >= 30 {
					h.State = "quiet"
					h.Detail = fmt.Sprintf("This source has not published in %d days.", days)
					h.Action = "Unsubscribe"
				}
			}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
