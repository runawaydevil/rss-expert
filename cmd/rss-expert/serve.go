package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/runawaydevil/rss-expert/internal/config"
	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/poller"
	"github.com/runawaydevil/rss-expert/internal/store"
	"github.com/runawaydevil/rss-expert/internal/web"
)

const shutdownGrace = 15 * time.Second

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	skipMigrate := fs.Bool("no-migrate", false, "refuse to start instead of applying pending migrations")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.RequireServing(); err != nil {
		return err
	}

	log := cfg.Logger(os.Stdout)
	log.Info("starting", "version", versionString(), "domain", cfg.Domain, "data", cfg.DataDir)

	db, err := store.OpenWith(ctx, cfg.DatabasePath(), cfg.CacheMiB)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := prepareSchema(ctx, db, log, *skipMigrate); err != nil {
		return err
	}

	accounts := identity.NewStore(db)
	if _, err := identity.Bootstrap(ctx, accounts, cfg.AdminEmail, cfg.AdminPassword, log); err != nil {
		return err
	}

	instance := web.New(db, log, "https://"+cfg.Domain, web.Options{
		MediaRoot:    filepath.Join(cfg.DataDir, "media"),
		MediaQuota:   cfg.MediaQuota,
		BehindProxy:  cfg.BehindProxy,
		ShowPreview:  cfg.ShowPreview,
		Version:      version,
		MetricsToken: cfg.MetricsToken,
	})

	app := &http.Server{
		Addr:              cfg.Listen,
		Handler:           instance.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	deliveryCtx, stopDelivery := context.WithCancel(ctx)
	defer stopDelivery()
	go web.NewDeliverer(instance).Run(deliveryCtx)

	feeds := poller.New(ingest.NewStore(db), log, poller.Options{
		UserAgent: "rss-expert/" + version + " (+https://" + cfg.Domain + ")",
		Workers:   cfg.PollWorkers,
		MaxBytes:  cfg.FetchLimit,
	})
	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	go feeds.Run(pollCtx)

	upkeepCtx, stopUpkeep := context.WithCancel(ctx)
	defer stopUpkeep()
	go upkeep{
		accounts: accounts,
		sources:  ingest.NewStore(db),
		queue:    jobs.New(db),
		log:      log,
	}.run(upkeepCtx)

	errs := make(chan error, 1)
	go listen(app, "app", cfg.Listen, log, errs)

	select {
	case err := <-errs:
		shutdown(app)
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdown(app)
		return nil
	}
}

func prepareSchema(ctx context.Context, db *store.DB, log logger, refuse bool) error {
	state, err := db.MigrationState(ctx)
	if err != nil {
		return err
	}
	if state.UpToDate() {
		log.Info("schema ready", "version", state.Applied)
		return nil
	}
	if refuse {
		return errors.New("schema is behind and --no-migrate was given; run: rss-expert migrate")
	}

	log.Info("applying migrations", "from", state.Applied, "to", state.Latest, "pending", len(state.Pending))
	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	log.Info("schema ready", "version", state.Latest, "applied", len(applied))
	debug.FreeOSMemory()
	return nil
}

type logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

func listen(srv *http.Server, name, address string, log logger, errs chan<- error) {
	log.Info("listening", "server", name, "address", address)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs <- err
	}
}

func shutdown(servers ...*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	for _, srv := range servers {
		srv.Shutdown(ctx)
	}
}
