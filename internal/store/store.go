package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	DefaultCacheMiB = 20
	readerConns     = 4
	busyTimeoutMS   = 5000
)

type DB struct {
	Write *sql.DB
	Read  *sql.DB
	Path  string
}

func Open(ctx context.Context, path string) (*DB, error) {
	return OpenWith(ctx, path, DefaultCacheMiB)
}

func OpenWith(ctx context.Context, path string, cacheMiB int) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("store: create data directory: %w", err)
	}
	if cacheMiB <= 0 {
		cacheMiB = DefaultCacheMiB
	}

	writerCacheKiB := cacheMiB * 1024
	readerCacheKiB := writerCacheKiB * 2 / 5

	write, err := open(ctx, path, writerCacheKiB)
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)

	read, err := open(ctx, path, readerCacheKiB)
	if err != nil {
		write.Close()
		return nil, err
	}
	read.SetMaxOpenConns(readerConns)
	read.SetMaxIdleConns(readerConns)

	return &DB{Write: write, Read: read, Path: path}, nil
}

func open(ctx context.Context, path string, cacheKiB int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, cacheKiB))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return db, nil
}

func dsn(path string, cacheKiB int) string {
	pragmas := []string{
		fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS),
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		fmt.Sprintf("cache_size(-%d)", cacheKiB),
		"temp_store(MEMORY)",
		"foreign_keys(ON)",
	}
	var b strings.Builder
	b.WriteString("file:")
	b.WriteString(filepath.ToSlash(path))
	for i, p := range pragmas {
		if i == 0 {
			b.WriteString("?_pragma=")
		} else {
			b.WriteString("&_pragma=")
		}
		b.WriteString(p)
	}
	return b.String()
}

func (db *DB) Close() error {
	var first error
	for _, h := range []*sql.DB{db.Read, db.Write} {
		if h == nil {
			continue
		}
		if err := h.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (db *DB) Pragmas(ctx context.Context, pool *sql.DB) (map[string]string, error) {
	names := []string{"journal_mode", "synchronous", "busy_timeout", "cache_size", "temp_store", "foreign_keys", "page_size"}
	out := make(map[string]string, len(names))
	for _, name := range names {
		var value string
		if err := pool.QueryRowContext(ctx, "pragma "+name).Scan(&value); err != nil {
			return out, fmt.Errorf("store: read pragma %s: %w", name, err)
		}
		out[name] = value
	}
	return out, nil
}

func (db *DB) SizeOnDisk() (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(db.Path + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return total, err
		}
		total += info.Size()
	}
	return total, nil
}
