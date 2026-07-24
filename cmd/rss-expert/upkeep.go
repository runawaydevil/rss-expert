package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
)

const (
	upkeepInterval  = 6 * time.Hour
	finishedJobsAge = 14 * 24 * time.Hour
)

type upkeep struct {
	accounts *identity.Store
	sources  *ingest.Store
	queue    *jobs.Queue
	log      *slog.Logger
}

func (u upkeep) run(ctx context.Context) {
	ticker := time.NewTicker(upkeepInterval)
	defer ticker.Stop()

	u.once(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.once(ctx)
		}
	}
}

func (u upkeep) once(ctx context.Context) {
	if n, err := u.accounts.PurgeExpiredSessions(ctx); err != nil {
		u.log.Warn("could not purge expired sessions", "error", err)
	} else if n > 0 {
		u.log.Info("purged expired sessions", "count", n)
	}

	if n, err := u.queue.PurgeDone(ctx, finishedJobsAge); err != nil {
		u.log.Warn("could not purge finished jobs", "error", err)
	} else if n > 0 {
		u.log.Info("purged finished jobs", "count", n)
	}

	if n, err := u.sources.PurgeExpiredPayloads(ctx, time.Now().UTC()); err != nil {
		u.log.Warn("could not purge stored payloads", "error", err)
	} else if n > 0 {
		u.log.Info("purged stored payloads past their retention", "count", n)
	}
}
