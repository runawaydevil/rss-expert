package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/push"
	"github.com/runawaydevil/rss-expert/internal/store"
	"github.com/runawaydevil/rss-expert/internal/web"
)

const (
	upkeepInterval  = 1 * time.Hour
	finishedJobsAge = 14 * 24 * time.Hour
	seenActivityAge = 30 * 24 * time.Hour
)

type upkeep struct {
	instance *web.App
	db       *store.DB
	ap       *activitypub.Store
	push     *push.Store
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

	if n, err := u.accounts.PurgeExpiredTokens(ctx, time.Now().UTC()); err != nil {
		u.log.Warn("could not purge expired email tokens", "error", err)
	} else if n > 0 {
		u.log.Info("purged used and expired email tokens", "count", n)
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

	if n, err := u.push.ForgetExpired(ctx, time.Now().UTC()); err != nil {
		u.log.Warn("could not forget expired subscribers", "error", err)
	} else if n > 0 {
		u.log.Info("forgot subscribers whose lease ran out", "count", n)
	}

	if n, err := u.ap.ForgetOldActivities(ctx, seenActivityAge); err != nil {
		u.log.Warn("could not forget old activity ids", "error", err)
	} else if n > 0 {
		u.log.Info("forgot old activity ids", "count", n)
	}

	if n := u.instance.RenewPush(ctx); n > 0 {
		u.log.Info("renewed push subscriptions", "count", n)
	}

	if err := u.db.Optimize(ctx); err != nil {
		u.log.Warn("could not run sqlite optimize", "error", err)
	}
}
