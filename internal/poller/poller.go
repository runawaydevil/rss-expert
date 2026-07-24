package poller

import (
	"context"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/feedin"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/safety"
)

const (
	DefaultWorkers = 4
	DefaultBatch   = 32
	SweepInterval  = time.Minute
	maxJitter      = 90 * time.Second
)

var feedContentTypes = []string{
	"application/rss+xml",
	"application/atom+xml",
	"application/xml",
	"text/xml",
	"application/feed+json",
	"application/json",
	"text/html",
	"application/rdf+xml",
}

type Poller struct {
	sources *ingest.Store
	fetcher *safety.Fetcher
	log     *slog.Logger
	workers int
	batch   int
}

type Options struct {
	Workers           int
	Batch             int
	MaxBytes          int64
	UserAgent         string
	AllowPrivateAddrs bool
}

func New(sources *ingest.Store, log *slog.Logger, o Options) *Poller {
	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}
	if o.Batch <= 0 {
		o.Batch = DefaultBatch
	}
	if o.UserAgent == "" {
		o.UserAgent = "rss-expert"
	}

	return &Poller{
		sources: sources,
		log:     log,
		workers: o.Workers,
		batch:   o.Batch,
		fetcher: safety.New(safety.Options{
			UserAgent:          o.UserAgent,
			AcceptContentTypes: feedContentTypes,
			AllowPrivateAddrs:  o.AllowPrivateAddrs,
			MaxBytes:           o.MaxBytes,
		}),
	}
}

func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()

	p.log.Info("poller started", "workers", p.workers, "sweep", SweepInterval)

	for {
		if read, err := p.Once(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Error("sweep failed", "error", err)
		} else if read > 0 {
			p.log.Info("sweep finished", "sources", read)
		}

		select {
		case <-ctx.Done():
			p.log.Info("poller stopped")
			return
		case <-ticker.C:
		}
	}
}

func (p *Poller) Once(ctx context.Context) (int, error) {
	due, err := p.sources.Due(ctx, time.Now().UTC(), p.batch)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}

	work := make(chan *ingest.Source)
	var wg sync.WaitGroup

	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for source := range work {
				if err := p.Fetch(ctx, source); err != nil && ctx.Err() == nil {
					p.log.Warn("could not read source", "feed", source.FeedURL, "error", err)
				}
			}
		}()
	}

	for _, source := range due {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return 0, ctx.Err()
		case work <- source:
		}
	}
	close(work)
	wg.Wait()

	return len(due), nil
}

func (p *Poller) Fetch(ctx context.Context, source *ingest.Source) error {
	now := time.Now().UTC()

	header := http.Header{}
	if source.ETag != "" {
		header.Set("If-None-Match", source.ETag)
	}
	if source.LastModified != "" {
		header.Set("If-Modified-Since", source.LastModified)
	}

	result, err := p.fetcher.Get(ctx, source.FeedURL, header)
	if err != nil {
		return p.sources.RecordFetch(ctx, source.ID, ingest.FetchOutcome{Err: err}, jitter(now))
	}

	outcome := ingest.FetchOutcome{
		Status:       result.StatusCode,
		ETag:         result.ETag(),
		LastModified: result.LastModified(),
	}

	if result.NotModified() {
		p.log.Debug("source unchanged", "feed", source.FeedURL)
		return p.sources.RecordFetch(ctx, source.ID, outcome, jitter(now))
	}

	parsed, err := feedin.Parse(result.Body)
	if err != nil {
		outcome.Err = err
		return p.sources.RecordFetch(ctx, source.ID, outcome, jitter(now))
	}

	outcome.Cadence = cadence(parsed)

	ingested, err := p.sources.Ingest(ctx, source, result.Body,
		result.Header.Get("Content-Type"), parsed)
	if err != nil {
		outcome.Err = err
		p.sources.RecordFetch(ctx, source.ID, outcome, jitter(now))
		return err
	}

	if ingested.Observations > 0 {
		p.log.Info("read source",
			"feed", source.FeedURL,
			"new", ingested.Observations,
			"converged", ingested.Converged)
	}
	return p.sources.RecordFetch(ctx, source.ID, outcome, jitter(now))
}

func cadence(f *feed.Feed) time.Duration {
	times := make([]time.Time, 0, len(f.Items))
	for i := range f.Items {
		if t := f.Items[i].Published; !t.IsZero() {
			times = append(times, t)
		}
	}
	if len(times) < 3 {
		return 0
	}

	sort.Slice(times, func(i, j int) bool { return times[i].After(times[j]) })

	gaps := make([]time.Duration, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		if gap := times[i-1].Sub(times[i]); gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 0
	}

	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[len(gaps)/2]
}

func jitter(now time.Time) time.Time {
	return now.Add(time.Duration(rand.Int63n(int64(maxJitter))))
}
