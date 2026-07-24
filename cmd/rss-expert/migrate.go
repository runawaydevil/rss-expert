package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/runawaydevil/rss-expert/internal/config"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func migrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "report what would be applied and change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	state, err := db.MigrationState(ctx)
	if err != nil {
		return err
	}

	if state.UpToDate() {
		fmt.Printf("schema is at version %d, nothing pending\n", state.Applied)
		return nil
	}

	if *dryRun {
		fmt.Printf("schema is at version %d, latest is %d\n", state.Applied, state.Latest)
		for _, v := range state.Pending {
			fmt.Printf("  would apply %d\n", v)
		}
		return nil
	}

	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	for _, v := range applied {
		fmt.Printf("applied %d\n", v)
	}
	fmt.Printf("schema is at version %d\n", state.Latest)
	return nil
}
