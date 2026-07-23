package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type MigrationState struct {
	Applied int64
	Latest  int64
	Pending []int64
}

func (s MigrationState) UpToDate() bool { return len(s.Pending) == 0 }

func (db *DB) provider() (*goose.Provider, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db.Write, sub)
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	return p, nil
}

func (db *DB) MigrationState(ctx context.Context) (MigrationState, error) {
	var state MigrationState

	p, err := db.provider()
	if err != nil {
		return state, err
	}

	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return state, fmt.Errorf("store: read migration versions: %w", err)
	}
	state.Applied = current
	state.Latest = target

	status, err := p.Status(ctx)
	if err != nil {
		return state, fmt.Errorf("store: read migration status: %w", err)
	}
	for _, s := range status {
		if s.State == goose.StatePending {
			state.Pending = append(state.Pending, s.Source.Version)
		}
	}
	return state, nil
}

func (db *DB) Migrate(ctx context.Context) ([]int64, error) {
	p, err := db.provider()
	if err != nil {
		return nil, err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
	}
	return applied, nil
}
