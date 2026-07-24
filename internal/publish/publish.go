package publish

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/media"
	"github.com/runawaydevil/rss-expert/internal/store"
)

const (
	MaxMarkdownBytes = 64 * 1024
	MaxTitleRunes    = 200
)

var (
	ErrEmptyPost   = errors.New("a post needs something in it")
	ErrPostTooLong = fmt.Errorf("a post cannot be longer than %d bytes", MaxMarkdownBytes)
	ErrNoPost      = errors.New("no such post")
	ErrNotYours    = errors.New("that post belongs to someone else")
)

var (
	markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))
	sanitise = bluemonday.UGCPolicy()
)

type Post struct {
	ID         int64
	AccountID  int64
	Handle     string
	Author     string
	GUID       string
	Title      string
	Markdown   string
	HTML       string
	InReplyTo  string
	Published  time.Time
	Updated    time.Time
	Deleted    bool
	ReplyCount int
	Media      []*media.File
}

func (p *Post) Edited() bool {
	return !p.Updated.IsZero() && p.Updated.After(p.Published)
}

type Store struct {
	files    *media.Store
	db       *store.DB
	accounts *identity.Store
	sources  *ingest.Store
	base     string
	domain   string
}

func NewStore(db *store.DB, baseURL string) *Store {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if !strings.Contains(base, "//") {
		base = "https://" + base
	}

	domain := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		domain = u.Host
	}

	return &Store{
		files:    media.New(db, media.Options{}),
		db:       db,
		accounts: identity.NewStore(db),
		sources:  ingest.NewStore(db),
		base:     base,
		domain:   domain,
	}
}

func (s *Store) project(ctx context.Context, post *Post) error {
	source, err := s.sources.EnsureLocalSource(ctx,
		s.AccountFeedURL(post.Handle), post.Handle+" on "+s.domain)
	if err != nil {
		return err
	}
	item := s.item(post)
	return s.sources.IngestItem(ctx, source, []byte(post.Markdown), &item)
}

var _ = feed.Item{}

func (s *Store) baseURL() string { return s.base }

func (s *Store) BaseURL() string { return s.base }

func (s *Store) PostURL(id int64) string {
	return fmt.Sprintf("%s/p/%d", s.baseURL(), id)
}

func (s *Store) RepliesURL(id int64) string {
	return fmt.Sprintf("%s/p/%d/replies.xml", s.baseURL(), id)
}

func (s *Store) AccountFeedURL(handle string) string {
	return fmt.Sprintf("%s/users/%s/rss.xml", s.baseURL(), handle)
}

func (s *Store) FirehoseURL() string {
	return s.baseURL() + "/users/rss.xml"
}

func Render(source string) (string, error) {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(source), &buf); err != nil {
		return "", err
	}
	return strings.TrimSpace(sanitise.Sanitize(buf.String())), nil
}

func (s *Store) Create(ctx context.Context, account *identity.Account, title, source, inReplyTo string) (*Post, error) {
	source = strings.TrimSpace(source)
	title = strings.TrimSpace(title)

	if source == "" {
		return nil, ErrEmptyPost
	}
	if len(source) > MaxMarkdownBytes {
		return nil, ErrPostTooLong
	}
	if len([]rune(title)) > MaxTitleRunes {
		title = string([]rune(title)[:MaxTitleRunes])
	}

	html, err := Render(source)
	if err != nil {
		return nil, err
	}
	if html == "" {
		return nil, ErrEmptyPost
	}

	handle, err := s.EnsureHandle(ctx, account)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Truncate(time.Second)
	res, err := s.db.Write.ExecContext(ctx,
		`insert into post (account_id, guid, title, markdown, html, in_reply_to, published_at)
		 values (?, '', ?, ?, ?, ?, ?)`,
		account.ID, nullable(title), source, html, nullable(inReplyTo), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("publish: create post: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	guid := s.PostURL(id)
	if _, err := s.db.Write.ExecContext(ctx, `update post set guid = ? where id = ?`, guid, id); err != nil {
		return nil, err
	}

	post := &Post{
		ID: id, AccountID: account.ID, Handle: handle, Author: account.Email,
		GUID: guid, Title: title, Markdown: source, HTML: html,
		InReplyTo: inReplyTo, Published: now,
	}
	if err := s.project(ctx, post); err != nil {
		return nil, fmt.Errorf("publish: project into the timeline: %w", err)
	}
	return post, nil
}

func (s *Store) Update(ctx context.Context, account *identity.Account, id int64, title, source string) (*Post, error) {
	post, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if post.AccountID != account.ID {
		return nil, ErrNotYours
	}

	source = strings.TrimSpace(source)
	if source == "" {
		return nil, ErrEmptyPost
	}
	if len(source) > MaxMarkdownBytes {
		return nil, ErrPostTooLong
	}

	html, err := Render(source)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Truncate(time.Second)
	if !now.After(post.Published) {
		now = post.Published.Add(time.Second)
	}
	if !post.Updated.IsZero() && !now.After(post.Updated) {
		now = post.Updated.Add(time.Second)
	}

	if _, err := s.db.Write.ExecContext(ctx,
		`update post set title = ?, markdown = ?, html = ?, updated_at = ? where id = ?`,
		nullable(strings.TrimSpace(title)), source, html, now.Unix(), id); err != nil {
		return nil, fmt.Errorf("publish: update post: %w", err)
	}

	post.Title = strings.TrimSpace(title)
	post.Markdown = source
	post.HTML = html
	post.Updated = now
	if err := s.project(ctx, post); err != nil {
		return nil, fmt.Errorf("publish: project the edit: %w", err)
	}
	return post, nil
}

func (s *Store) Delete(ctx context.Context, account *identity.Account, id int64) error {
	post, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	if post.AccountID != account.ID && !account.Role.CanModerate() {
		return ErrNotYours
	}
	_, err = s.db.Write.ExecContext(ctx,
		`update post set deleted_at = ? where id = ?`, time.Now().UTC().Unix(), id)
	return err
}

const postColumns = `p.id, p.account_id, coalesce(a.handle, ''), a.email, p.guid,
	coalesce(p.title, ''), p.markdown, p.html, coalesce(p.in_reply_to, ''),
	p.published_at, coalesce(p.updated_at, 0), p.deleted_at is not null,
	(select count(*) from post r where r.in_reply_to = p.guid and r.deleted_at is null)`

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	var (
		p                  Post
		published, updated int64
	)
	err := row.Scan(&p.ID, &p.AccountID, &p.Handle, &p.Author, &p.GUID, &p.Title,
		&p.Markdown, &p.HTML, &p.InReplyTo, &published, &updated, &p.Deleted, &p.ReplyCount)
	if err != nil {
		return nil, err
	}
	p.Published = time.Unix(published, 0).UTC()
	if updated > 0 {
		p.Updated = time.Unix(updated, 0).UTC()
	}
	return &p, nil
}

func (s *Store) ByID(ctx context.Context, id int64) (*Post, error) {
	row := s.db.Read.QueryRowContext(ctx,
		`select `+postColumns+` from post p join account a on a.id = p.account_id where p.id = ?`, id)
	post, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoPost
	}
	if err != nil {
		return nil, err
	}
	return s.withMedia(ctx, []*Post{post})[0], nil
}

func (s *Store) query(ctx context.Context, where string, args ...any) ([]*Post, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+postColumns+` from post p join account a on a.id = p.account_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Post
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, post)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.withMedia(ctx, out), nil
}

func (s *Store) ByHandle(ctx context.Context, handle string, limit int) ([]*Post, error) {
	return s.query(ctx,
		`where a.handle = ? and p.deleted_at is null order by p.published_at desc limit ?`,
		handle, limit)
}

func (s *Store) Recent(ctx context.Context, limit int) ([]*Post, error) {
	return s.query(ctx, `where p.deleted_at is null order by p.published_at desc limit ?`, limit)
}

func (s *Store) Replies(ctx context.Context, guid string, limit int) ([]*Post, error) {
	return s.query(ctx,
		`where p.in_reply_to = ? and p.deleted_at is null order by p.published_at limit ?`,
		guid, limit)
}

func (s *Store) EnsureHandle(ctx context.Context, account *identity.Account) (string, error) {
	var existing sql.NullString
	err := s.db.Read.QueryRowContext(ctx, `select handle from account where id = ?`, account.ID).Scan(&existing)
	if err != nil {
		return "", err
	}
	if existing.Valid && existing.String != "" {
		return existing.String, nil
	}

	base := handleFrom(account.Email)
	for attempt := 0; attempt < 50; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = base + strconv.Itoa(attempt+1)
		}
		_, err := s.db.Write.ExecContext(ctx, `update account set handle = ? where id = ?`, candidate, account.ID)
		if err == nil {
			return candidate, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return "", err
		}
	}
	return "", errors.New("publish: could not find a free handle")
}

func handleFrom(email string) string {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")

	var b strings.Builder
	for _, r := range local {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}

	handle := strings.Trim(b.String(), "-")
	if handle == "" {
		return "someone"
	}
	if len(handle) > 32 {
		handle = handle[:32]
	}
	return handle
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) ByGUID(ctx context.Context, guid string) (*Post, error) {
	row := s.db.Read.QueryRowContext(ctx,
		`select `+postColumns+` from post p join account a on a.id = p.account_id where p.guid = ?`, guid)
	post, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoPost
	}
	return post, err
}

func (s *Store) withMedia(ctx context.Context, posts []*Post) []*Post {
	if len(posts) == 0 {
		return posts
	}

	ids := make([]int64, len(posts))
	for i, post := range posts {
		ids[i] = post.ID
	}

	byPost, err := s.files.ForPosts(ctx, ids)
	if err != nil || len(byPost) == 0 {
		return posts
	}
	for _, post := range posts {
		post.Media = byPost[post.ID]
	}
	return posts
}

func (s *Store) HandleFor(ctx context.Context, accountID int64) (string, error) {
	var handle sql.NullString
	err := s.db.Read.QueryRowContext(ctx,
		`select handle from account where id = ?`, accountID).Scan(&handle)
	if err != nil {
		return "", err
	}
	if !handle.Valid || handle.String == "" {
		return "", ErrNoPost
	}
	return handle.String, nil
}

type DraftText struct {
	Title     string
	Markdown  string
	InReplyTo string
}

func (s *Store) SaveDraft(ctx context.Context, accountID int64, title, markdown, inReplyTo string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`insert into draft (account_id, title, markdown, in_reply_to, saved_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (account_id) do update set
		   title = excluded.title, markdown = excluded.markdown,
		   in_reply_to = excluded.in_reply_to, saved_at = excluded.saved_at`,
		accountID, nullable(title), markdown, nullable(inReplyTo), time.Now().UTC().Unix())
	return err
}

func (s *Store) Draft(ctx context.Context, accountID int64) (DraftText, error) {
	var draft DraftText
	err := s.db.Read.QueryRowContext(ctx,
		`select coalesce(title, ''), markdown, coalesce(in_reply_to, '')
		 from draft where account_id = ?`, accountID).
		Scan(&draft.Title, &draft.Markdown, &draft.InReplyTo)
	return draft, err
}

func (s *Store) DropDraft(ctx context.Context, accountID int64) error {
	_, err := s.db.Write.ExecContext(ctx, `delete from draft where account_id = ?`, accountID)
	return err
}
