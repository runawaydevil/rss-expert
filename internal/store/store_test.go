package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPragmasTakeEffect(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	writer, err := db.Pragmas(ctx, db.Write)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := db.Pragmas(ctx, db.Read)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1",
		"busy_timeout": "5000",
		"foreign_keys": "1",
		"temp_store":   "2",
	} {
		if got := writer[name]; got != want {
			t.Errorf("writer %s = %q, want %q", name, got, want)
		}
		if got := reader[name]; got != want {
			t.Errorf("reader %s = %q, want %q", name, got, want)
		}
	}

	if writer["cache_size"] != "-20000" {
		t.Errorf("writer cache_size = %q, want -20000", writer["cache_size"])
	}
	if reader["cache_size"] != "-8000" {
		t.Errorf("reader cache_size = %q, want -8000", reader["cache_size"])
	}
}

func TestWriterPoolIsSerialised(t *testing.T) {
	db := openTemp(t)
	if got := db.Write.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := db.Read.Stats().MaxOpenConnections; got != readerConns {
		t.Errorf("reader MaxOpenConnections = %d, want %d", got, readerConns)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	before, err := db.MigrationState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.UpToDate() {
		t.Fatal("a fresh database reports no pending migrations")
	}
	if before.Applied != 0 {
		t.Errorf("fresh database is at version %d, want 0", before.Applied)
	}

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(before.Pending) {
		t.Errorf("applied %d migrations, %d were pending", len(applied), len(before.Pending))
	}

	after, err := db.MigrationState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpToDate() {
		t.Errorf("still pending after migrate: %v", after.Pending)
	}
	if after.Applied != before.Latest {
		t.Errorf("applied version = %d, want %d", after.Applied, before.Latest)
	}

	again, err := db.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second migrate applied %v, want nothing", again)
	}
}

func TestConcurrentReadsDuringWrite(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, err := db.Write.ExecContext(ctx,
				`insert into instance (key, value, updated_at) values (?, ?, unixepoch())
				 on conflict (key) do update set value = excluded.value, updated_at = excluded.updated_at`,
				"probe", i)
			if err != nil {
				errs <- err
				return
			}
		}
	}()

	for r := 0; r < readerConns; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				var count int
				if err := db.Read.QueryRowContext(ctx, `select count(*) from instance`).Scan(&count); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent access failed: %v", err)
	}
}

func TestSizeOnDiskCountsWAL(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	size, err := db.SizeOnDisk()
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Errorf("size on disk = %d, want more than zero", size)
	}
}
