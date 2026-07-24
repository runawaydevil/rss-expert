package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

const ManifestName = "manifest.json"

var (
	ErrNotABackup       = errors.New("that directory does not hold a backup manifest")
	ErrCorrupt          = errors.New("a file in the backup does not match its recorded hash")
	ErrSchemaTooNew     = errors.New("the backup was taken by a newer schema than this binary knows")
	ErrTargetNotEmpty   = errors.New("the destination already holds a database; move it aside first")
	ErrManifestMismatch = errors.New("the manifest lists a file that is not in the backup")
)

type File struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	TakenAt       time.Time `json:"taken_at"`
	AppVersion    string    `json:"app_version"`
	SchemaVersion int64     `json:"schema_version"`
	Files         []File    `json:"files"`
	Media         []File    `json:"media,omitempty"`
	MediaReused   int       `json:"media_reused,omitempty"`
}

func Take(ctx context.Context, db *store.DB, into, appVersion string) (*Manifest, error) {
	return TakeWithMedia(ctx, db, into, appVersion, "")
}

func TakeWithMedia(ctx context.Context, db *store.DB, into, appVersion, mediaDir string) (*Manifest, error) {
	if err := os.MkdirAll(into, 0o750); err != nil {
		return nil, err
	}

	state, err := db.MigrationState(ctx)
	if err != nil {
		return nil, err
	}
	if !state.UpToDate() {
		return nil, fmt.Errorf("backup: the schema is behind (%d of %d); migrate before taking a backup",
			state.Applied, state.Latest)
	}

	target := filepath.Join(into, "rss-expert.db")
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := db.Write.ExecContext(ctx, `vacuum into ?`, target); err != nil {
		return nil, fmt.Errorf("backup: vacuum into %s: %w", target, err)
	}

	manifest := &Manifest{
		TakenAt:       time.Now().UTC(),
		AppVersion:    appVersion,
		SchemaVersion: state.Applied,
	}

	entry, err := describe(target)
	if err != nil {
		return nil, err
	}
	manifest.Files = append(manifest.Files, entry)

	if mediaDir != "" {
		files, reused, err := copyMedia(mediaDir, into)
		if err != nil {
			return nil, err
		}
		manifest.Media, manifest.MediaReused = files, reused
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(into, ManifestName), append(body, '\n'), 0o600); err != nil {
		return nil, err
	}
	return manifest, nil
}

func Read(from string) (*Manifest, error) {
	body, err := os.ReadFile(filepath.Join(from, ManifestName))
	if os.IsNotExist(err) {
		return nil, ErrNotABackup
	}
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("backup: unreadable manifest: %w", err)
	}
	return &manifest, nil
}

func Verify(from string) (*Manifest, error) {
	manifest, err := Read(from)
	if err != nil {
		return nil, err
	}
	if len(manifest.Files) == 0 {
		return nil, ErrManifestMismatch
	}

	for _, want := range append(append([]File{}, manifest.Files...), manifest.Media...) {
		path := filepath.Join(from, filepath.FromSlash(want.Name))
		got, err := describe(path)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrManifestMismatch, want.Name)
		}
		if err != nil {
			return nil, err
		}
		if got.SHA256 != want.SHA256 || got.Bytes != want.Bytes {
			return nil, fmt.Errorf("%w: %s", ErrCorrupt, want.Name)
		}
	}
	return manifest, nil
}

func Restore(ctx context.Context, from, dataDir, appVersion string) (*Manifest, error) {
	manifest, err := Verify(from)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	target := filepath.Join(dataDir, "rss-expert.db")
	if _, err := os.Stat(target); err == nil {
		return nil, ErrTargetNotEmpty
	}

	for _, file := range manifest.Files {
		if err := copyFile(filepath.Join(from, file.Name), filepath.Join(dataDir, file.Name)); err != nil {
			return nil, err
		}
	}

	for _, file := range manifest.Media {
		name := filepath.FromSlash(file.Name)
		target := filepath.Join(dataDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return nil, err
		}
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := copyFile(filepath.Join(from, name), target); err != nil {
			return nil, err
		}
	}

	db, err := store.Open(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("backup: the restored database will not open: %w", err)
	}
	defer db.Close()

	state, err := db.MigrationState(ctx)
	if err != nil {
		return nil, err
	}
	if state.Applied > state.Latest {
		return nil, fmt.Errorf("%w: backup is at %d, this binary knows %d",
			ErrSchemaTooNew, state.Applied, state.Latest)
	}
	if !state.UpToDate() {
		if _, err := db.Migrate(ctx); err != nil {
			return nil, fmt.Errorf("backup: restored, but migrating it forward failed: %w", err)
		}
	}

	var count int
	if err := db.Read.QueryRowContext(ctx,
		`select count(*) from sqlite_master where type = 'table'`).Scan(&count); err != nil {
		return nil, fmt.Errorf("backup: the restored database is unreadable: %w", err)
	}
	if count == 0 {
		return nil, errors.New("backup: the restored database has no tables")
	}

	return manifest, nil
}

func copyMedia(mediaDir, into string) ([]File, int, error) {
	root, err := os.Stat(mediaDir)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !root.IsDir() {
		return nil, 0, fmt.Errorf("backup: %s is not a directory", mediaDir)
	}

	var (
		out    []File
		reused int
	)
	err = filepath.WalkDir(mediaDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		relative, err := filepath.Rel(mediaDir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join("media", relative))

		source, err := describe(path)
		if err != nil {
			return err
		}
		source.Name = name

		target := filepath.Join(into, filepath.FromSlash(name))
		if existing, err := describe(target); err == nil && existing.SHA256 == source.SHA256 {
			reused++
			out = append(out, source)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		out = append(out, source)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, reused, nil
}

func describe(path string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}

	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return File{}, err
	}
	return File{
		Name:   filepath.Base(path),
		Bytes:  info.Size(),
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Sync()
}
