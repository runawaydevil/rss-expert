package store

import (
	"context"
	"io/fs"
	"testing"
)

func TestEveryMigrationDeclaresBothDirections(t *testing.T) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(sub, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations were embedded")
	}

	for _, name := range names {
		body, err := fs.ReadFile(sub, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"-- +goose Up", "-- +goose Down"} {
			if !contains(string(body), marker) {
				t.Errorf("%s has no %q section; a migration that cannot be undone is not one we ship", name, marker)
			}
		}
	}
}

func TestSchemaRollsAllTheWayDownAndBackUp(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("nothing was applied")
	}

	state, err := db.MigrationState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	top := state.Applied

	provider, err := db.provider()
	if err != nil {
		t.Fatal(err)
	}

	for version := top; version > 0; version-- {
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("rolling back from %d failed: %v", version, err)
		}
	}

	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != 0 {
		t.Fatalf("after rolling everything back the schema is at %d", current)
	}

	var tables int
	if err := db.Read.QueryRowContext(ctx,
		`select count(*) from sqlite_master
		 where type = 'table' and name not like 'sqlite_%' and name <> 'goose_db_version'`).
		Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("%d tables survived a full rollback; every down migration should clean up after itself", tables)
	}

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrating back up after a full rollback failed: %v", err)
	}
	state, err = db.MigrationState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Applied != top || !state.UpToDate() {
		t.Errorf("after up-down-up the schema is at %d, want %d", state.Applied, top)
	}
}

func TestEachStepRollsBackOnItsOwn(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	provider, err := db.provider()
	if err != nil {
		t.Fatal(err)
	}

	state, _ := db.MigrationState(ctx)
	for version := state.Applied; version > 0; version-- {
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("down from %d failed: %v", version, err)
		}
		if _, err := provider.Up(ctx); err != nil {
			t.Fatalf("up again after undoing %d failed: %v", version, err)
		}
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("second down from %d failed: %v", version, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
