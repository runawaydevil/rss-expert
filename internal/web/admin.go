package web

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/store"
)

type ops struct {
	db       *store.DB
	version  string
	token    string
	registry *prometheus.Registry
}

func newOps(db *store.DB, version, token string) *ops {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rss_expert_build_info",
		Help: "Build information, always 1.",
	}, []string{"version", "go_version"})
	buildInfo.WithLabelValues(version, runtime.Version()).Set(1)
	registry.MustRegister(buildInfo)

	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "rss_expert_database_bytes",
		Help: "Size of the database, write-ahead log and shared memory file on disk.",
	}, func() float64 {
		size, err := db.SizeOnDisk()
		if err != nil {
			return 0
		}
		return float64(size)
	}))

	queue := jobs.New(db)
	sources := ingest.NewStore(db)

	for name, help := range map[string]string{
		"ready":       "Jobs waiting to be claimed.",
		"leased":      "Jobs a worker is holding.",
		"dead_letter": "Jobs that gave up after every retry and need a human.",
	} {
		which := name
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "rss_expert_queue_" + which,
			Help: help,
		}, func() float64 {
			depth, err := queue.Depth(context.Background())
			if err != nil {
				return 0
			}
			switch which {
			case "ready":
				return float64(depth.Ready)
			case "leased":
				return float64(depth.Leased)
			}
			return float64(depth.DeadLetter)
		}))
	}

	for name, help := range map[string]string{
		"payloads":     "Raw payloads kept for reprocessing and audit.",
		"observations": "Readings recorded, one per source per version.",
		"items":        "Logical items after convergence.",
	} {
		which := name
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "rss_expert_store_" + which,
			Help: help,
		}, func() float64 {
			payloads, observations, items, err := sources.Counts(context.Background())
			if err != nil {
				return 0
			}
			switch which {
			case "payloads":
				return float64(payloads)
			case "observations":
				return float64(observations)
			}
			return float64(items)
		}))
	}

	return &ops{db: db, version: version, token: token, registry: registry}
}

func (o *ops) healthz(w http.ResponseWriter, r *http.Request) {
	plain(w, http.StatusOK, "ok")
}

func (o *ops) readyz(w http.ResponseWriter, r *http.Request) {
	if reason := o.notReady(r.Context()); reason != "" {
		plain(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	plain(w, http.StatusOK, "ready")
}

func (o *ops) notReady(ctx context.Context) string {
	if err := o.db.Read.PingContext(ctx); err != nil {
		return "database unreachable: " + err.Error()
	}

	state, err := o.db.MigrationState(ctx)
	if err != nil {
		return "migration state unknown: " + err.Error()
	}
	if !state.UpToDate() {
		return fmt.Sprintf("schema is at %d, this binary knows %d", state.Applied, state.Latest)
	}
	return ""
}

func (o *ops) metrics(w http.ResponseWriter, r *http.Request) {
	if o.token == "" {
		http.NotFound(w, r)
		return
	}

	sent := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if sent == "" {
		sent = r.URL.Query().Get("token")
	}
	if subtle.ConstantTimeCompare([]byte(o.token), []byte(sent)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		plain(w, http.StatusUnauthorized, "a token is required")
		return
	}

	promhttp.HandlerFor(o.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (o *ops) heapProfile(w http.ResponseWriter, r *http.Request) {
	if o.token == "" || subtle.ConstantTimeCompare([]byte(o.token), []byte(r.URL.Query().Get("token"))) != 1 {
		http.NotFound(w, r)
		return
	}
	pprof.Handler("heap").ServeHTTP(w, r)
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(body + "\n"))
}
