package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/runawaydevil/rss-expert/internal/backup"
	"github.com/runawaydevil/rss-expert/internal/config"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func backupCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	into := fs.String("into", "", "directory to write the backup into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *into == "" {
		return errors.New("usage: rss-expert backup --into <directory>")
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

	started := time.Now()
	manifest, err := backup.TakeWithMedia(ctx, db, *into, version, filepath.Join(cfg.DataDir, "media"))
	if err != nil {
		return err
	}

	var total int64
	for _, file := range manifest.Files {
		total += file.Bytes
	}
	for _, file := range manifest.Media {
		total += file.Bytes
	}
	fmt.Printf("wrote %d file(s) and %d upload(s), %.1f MiB, schema %d, in %s\n",
		len(manifest.Files), len(manifest.Media), float64(total)/(1<<20), manifest.SchemaVersion,
		time.Since(started).Round(time.Millisecond))
	if manifest.MediaReused > 0 {
		fmt.Printf("%d upload(s) were already there and were not copied again\n", manifest.MediaReused)
	}
	fmt.Printf("verify it before you trust it: rss-expert restore --from %s --check\n", *into)
	return nil
}

func restoreCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	from := fs.String("from", "", "directory holding the backup")
	check := fs.Bool("check", false, "verify the backup and change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("usage: rss-expert restore --from <directory> [--check]")
	}

	if *check {
		manifest, err := backup.Verify(*from)
		if err != nil {
			return err
		}
		fmt.Printf("backup is intact: taken %s by %s, schema %d\n",
			manifest.TakenAt.Format(time.RFC3339), manifest.AppVersion, manifest.SchemaVersion)
		for _, file := range manifest.Files {
			fmt.Printf("  %s  %.1f MiB  %s\n", file.Name, float64(file.Bytes)/(1<<20), file.SHA256[:16])
		}
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	manifest, err := backup.Restore(ctx, *from, cfg.DataDir, version)
	if err != nil {
		return err
	}
	fmt.Printf("restored into %s: taken %s, schema %d\n",
		cfg.DataDir, manifest.TakenAt.Format(time.RFC3339), manifest.SchemaVersion)
	return nil
}
