package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/runawaydevil/rss-expert/internal/config"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/poller"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func sources(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rss-expert sources <list|add|read|remove> [flags]")
	}

	command, rest := args[0], args[1:]
	switch command {
	case "list":
		return listSources(ctx)
	case "add":
		return addSource(ctx, rest)
	case "read":
		return readSources(ctx, rest)
	case "remove":
		return removeSources(ctx, rest)
	default:
		return fmt.Errorf("unknown sources command %q", command)
	}
}

func withSources(ctx context.Context, fn func(context.Context, *ingest.Store, config.Config) error) error {
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
	if !state.UpToDate() {
		return errors.New("the schema is behind; run: rss-expert migrate")
	}

	return fn(ctx, ingest.NewStore(db), cfg)
}

func listSources(ctx context.Context) error {
	return withSources(ctx, func(ctx context.Context, s *ingest.Store, _ config.Config) error {
		all, err := s.Sources(ctx)
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Println("no sources yet; add one with: rss-expert sources add <feed url>")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTITLE\tFEED\tLAST\tEVERY\tSTATE")
		for _, source := range all {
			last := "never"
			if !source.LastFetchAt.IsZero() {
				last = source.LastFetchAt.Format("2006-01-02 15:04")
			}
			state := "ok"
			switch {
			case source.Quarantined:
				state = "quarantined"
			case source.LastError != "":
				state = source.LastError
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
				source.ID, truncate(source.Title, 28), truncate(source.FeedURL, 44),
				last, source.PollInterval, truncate(state, 24))
		}
		return w.Flush()
	})
}

func addSource(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sources add", flag.ExitOnError)
	read := fs.Bool("read", true, "read the feed once after adding it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rss-expert sources add <feed url>")
	}

	return withSources(ctx, func(ctx context.Context, s *ingest.Store, cfg config.Config) error {
		for _, raw := range fs.Args() {
			source, err := s.AddSource(ctx, raw)
			if err != nil {
				return fmt.Errorf("%s: %w", raw, err)
			}
			fmt.Printf("source %d: %s\n", source.ID, source.FeedURL)

			if *read {
				if err := readOne(ctx, s, cfg, source); err != nil {
					fmt.Fprintf(os.Stderr, "  could not read it yet: %v\n", err)
				}
			}
		}
		return nil
	})
}

func readSources(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sources read", flag.ExitOnError)
	all := fs.Bool("all", false, "read every source, not only the ones that are due")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return withSources(ctx, func(ctx context.Context, s *ingest.Store, cfg config.Config) error {
		list, err := s.Sources(ctx)
		if err != nil {
			return err
		}
		if !*all {
			list, err = s.Due(ctx, time.Now().UTC(), 100)
			if err != nil {
				return err
			}
		}
		if len(list) == 0 {
			fmt.Println("nothing due")
			return nil
		}
		for _, source := range list {
			if err := readOne(ctx, s, cfg, source); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", source.FeedURL, err)
			}
		}
		return nil
	})
}

func removeSources(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sources remove", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rss-expert sources remove <source id> [source id...]")
	}

	return withSources(ctx, func(ctx context.Context, s *ingest.Store, _ config.Config) error {
		for _, raw := range fs.Args() {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("%q is not a source id", raw)
			}
			if err := s.RemoveSource(ctx, id); err != nil {
				return fmt.Errorf("source %d: %w", id, err)
			}
			fmt.Printf("removed source %d\n", id)
		}
		return nil
	})
}

func readOne(ctx context.Context, s *ingest.Store, cfg config.Config, source *ingest.Source) error {
	p := poller.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), poller.Options{
		UserAgent: "rss-expert/" + version + " (+https://" + cfg.Domain + ")",
	})
	if err := p.Fetch(ctx, source); err != nil {
		return err
	}

	refreshed, err := s.SourceByID(ctx, source.ID)
	if err != nil {
		return err
	}
	_, _, items, err := s.Counts(ctx)
	if err != nil {
		return err
	}

	status := fmt.Sprintf("HTTP %d", refreshed.LastStatus)
	if refreshed.LastError != "" {
		status = refreshed.LastError
	}
	fmt.Printf("  %s — %s, %d items in the database, next read in %s\n",
		truncate(refreshed.Title, 32), status, items, refreshed.PollInterval)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
