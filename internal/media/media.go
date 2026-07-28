package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

const (
	MaxUploadBytes  = 12 << 20
	DefaultQuota    = 512 << 20
	MaxDimension    = 12000
	directoryPrefix = 2
)

var (
	ErrTooLarge    = fmt.Errorf("media: a file cannot be larger than %d MiB", MaxUploadBytes>>20)
	ErrEmpty       = errors.New("media: there is nothing in that file")
	ErrUnsupported = errors.New("media: that kind of file is not accepted here")
	ErrNotAnImage  = errors.New("media: that says it is an image but does not decode as one")
	ErrTooBig      = errors.New("media: that image is too large to be real")
	ErrQuota       = errors.New("media: this account has no room left")
	ErrNotFound    = errors.New("media: no such file")
	ErrNotYours    = errors.New("media: that file belongs to another account")
)

var accepted = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"audio/mpeg": ".mp3",
	"audio/ogg":  ".ogg",
	"audio/mp4":  ".m4a",
	"video/mp4":  ".mp4",
	"video/webm": ".webm",
}

func Accepts(mediaType string) bool {
	_, ok := accepted[mediaType]
	return ok
}

func isImage(mediaType string) bool { return strings.HasPrefix(mediaType, "image/") }

type File struct {
	ID        int64
	AccountID int64
	SHA256    string
	MediaType string
	Bytes     int64
	Width     int
	Height    int
	Alt       string
	Name      string
	Stripped  bool
	CreatedAt time.Time
}

func (f *File) URL() string { return "/media/" + f.SHA256 }

func (f *File) IsImage() bool { return isImage(f.MediaType) }
func (f *File) IsAudio() bool { return strings.HasPrefix(f.MediaType, "audio/") }
func (f *File) IsVideo() bool { return strings.HasPrefix(f.MediaType, "video/") }

type Store struct {
	db    *store.DB
	root  string
	quota int64
}

type Options struct {
	Root  string
	Quota int64
}

func New(db *store.DB, o Options) *Store {
	if o.Quota <= 0 {
		o.Quota = DefaultQuota
	}
	return &Store{db: db, root: o.Root, quota: o.Quota}
}

func (s *Store) path(sum string) string {
	return filepath.Join(s.root, sum[:directoryPrefix], sum)
}

func (s *Store) Put(ctx context.Context, accountID int64, name string, body []byte, alt string) (*File, error) {
	if len(body) == 0 {
		return nil, ErrEmpty
	}
	if len(body) > MaxUploadBytes {
		return nil, ErrTooLarge
	}

	mediaType, _, _ := strings.Cut(http.DetectContentType(body), ";")
	mediaType = strings.TrimSpace(mediaType)
	if !Accepts(mediaType) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, mediaType)
	}

	width, height := 0, 0
	stripped := false

	if isImage(mediaType) {
		config, _, err := image.DecodeConfig(strings.NewReader(string(body)))
		if err != nil && mediaType != "image/webp" {
			return nil, ErrNotAnImage
		}
		if err == nil {
			width, height = config.Width, config.Height
			if width > MaxDimension || height > MaxDimension {
				return nil, ErrTooBig
			}
		}

		cleaned, changed, stripErr := Strip(mediaType, body)
		if stripErr == nil {
			body, stripped = cleaned, changed
		}
	}

	used, err := s.Used(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if used+int64(len(body)) > s.quota {
		return nil, ErrQuota
	}

	raw := sha256.Sum256(body)
	sum := hex.EncodeToString(raw[:])

	target := s.path(sum)
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if err := os.WriteFile(target, body, 0o640); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	_, err = s.db.Write.ExecContext(ctx,
		`insert into media (account_id, sha256, media_type, byte_length, stored_length,
		                    width, height, alt, original_name, stripped, created_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (account_id, sha256) do update set alt = excluded.alt`,
		accountID, raw[:], mediaType, len(body), len(body),
		zeroToNil(width), zeroToNil(height), nullable(alt), nullable(safeName(name)),
		boolToInt(stripped), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("media: record: %w", err)
	}
	return s.BySHA(ctx, accountID, sum)
}

func (s *Store) Open(sum string) (io.ReadSeekCloser, error) {
	if !plausibleSHA(sum) {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.path(sum))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return f, err
}

func (s *Store) Used(ctx context.Context, accountID int64) (int64, error) {
	var used sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`select sum(stored_length) from media where account_id = ?`, accountID).Scan(&used)
	return used.Int64, err
}

func (s *Store) Quota() int64 { return s.quota }

func (s *Store) Remaining(ctx context.Context, accountID int64) (int64, error) {
	used, err := s.Used(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if used >= s.quota {
		return 0, nil
	}
	return s.quota - used, nil
}

func columns(alias string) string {
	if alias != "" {
		alias += "."
	}
	return alias + "id, " + alias + "account_id, " + alias + "sha256, " + alias + "media_type, " +
		alias + "byte_length, coalesce(" + alias + "width, 0), coalesce(" + alias + "height, 0), " +
		"coalesce(" + alias + "alt, ''), coalesce(" + alias + "original_name, ''), " +
		alias + "stripped, " + alias + "created_at"
}

func scanWithID(row interface{ Scan(...any) error }, postID *int64) (*File, error) {
	return scanInto(row, postID)
}

func scan(row interface{ Scan(...any) error }) (*File, error) {
	return scanInto(row, nil)
}

func scanInto(row interface{ Scan(...any) error }, postID *int64) (*File, error) {
	var (
		f        File
		raw      []byte
		stripped int
		created  int64
	)

	targets := []any{&f.ID, &f.AccountID, &raw, &f.MediaType, &f.Bytes,
		&f.Width, &f.Height, &f.Alt, &f.Name, &stripped, &created}
	if postID != nil {
		targets = append([]any{postID}, targets...)
	}

	if err := row.Scan(targets...); err != nil {
		return nil, err
	}
	f.SHA256 = hex.EncodeToString(raw)
	f.Stripped = stripped == 1
	f.CreatedAt = time.Unix(created, 0).UTC()
	return &f, nil
}

func (s *Store) BySHA(ctx context.Context, accountID int64, sum string) (*File, error) {
	raw, err := hex.DecodeString(sum)
	if err != nil {
		return nil, ErrNotFound
	}
	row := s.db.Read.QueryRowContext(ctx,
		`select `+columns("")+` from media where account_id = ? and sha256 = ?`, accountID, raw)
	file, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return file, err
}

func (s *Store) AnyBySHA(ctx context.Context, sum string) (*File, error) {
	raw, err := hex.DecodeString(sum)
	if err != nil {
		return nil, ErrNotFound
	}
	row := s.db.Read.QueryRowContext(ctx,
		`select `+columns("")+` from media where sha256 = ? limit 1`, raw)
	file, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return file, err
}

func (s *Store) ForAccount(ctx context.Context, accountID int64, limit int) ([]*File, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+columns("")+` from media where account_id = ? order by created_at desc limit ?`,
		accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*File
	for rows.Next() {
		file, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
}

func (s *Store) Attach(ctx context.Context, postID, mediaID int64, position int) error {
	res, err := s.db.Write.ExecContext(ctx,
		`insert into post_media (post_id, media_id, position)
		 select p.id, m.id, ?
		 from post p join media m on m.id = ?
		 where p.id = ? and p.account_id = m.account_id
		 on conflict do nothing`,
		position, mediaID, postID)
	if err != nil {
		return err
	}
	if attached, _ := res.RowsAffected(); attached == 0 {
		var exists int
		err := s.db.Read.QueryRowContext(ctx,
			`select count(*) from post_media where post_id = ? and media_id = ?`,
			postID, mediaID).Scan(&exists)
		if err == nil && exists > 0 {
			return nil
		}
		return ErrNotYours
	}
	return nil
}

func (s *Store) ForPosts(ctx context.Context, postIDs []int64) (map[int64][]*File, error) {
	if len(postIDs) == 0 {
		return nil, nil
	}

	args := make([]any, len(postIDs))
	for i, id := range postIDs {
		args[i] = id
	}

	rows, err := s.db.Read.QueryContext(ctx,
		`select pm.post_id, `+columns("m")+`
		 from post_media pm join media m on m.id = pm.media_id
		 where pm.post_id in (`+placeholders(len(args))+`) order by pm.post_id, pm.position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]*File, len(postIDs))
	for rows.Next() {
		var postID int64
		file, err := scanWithID(rows, &postID)
		if err != nil {
			return nil, err
		}
		out[postID] = append(out[postID], file)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Store) ForPost(ctx context.Context, postID int64) ([]*File, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select `+columns("m")+`
		 from post_media pm join media m on m.id = pm.media_id
		 where pm.post_id = ? order by pm.position`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*File
	for rows.Next() {
		file, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
}

func plausibleSHA(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	for _, r := range sum {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func safeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func zeroToNil(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
