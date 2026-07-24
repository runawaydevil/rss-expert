package web

import (
	"context"
	"net/http"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/store"
)

type Admin struct {
	db       *store.DB
	version  string
	registry *prometheus.Registry
}

func NewAdmin(db *store.DB, version string) *Admin {
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

	return &Admin{db: db, version: version, registry: registry}
}

func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))
	return mux
}

func (a *Admin) healthz(w http.ResponseWriter, r *http.Request) {
	plain(w, http.StatusOK, "ok")
}

func (a *Admin) readyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := a.db.Read.PingContext(ctx); err != nil {
		plain(w, http.StatusServiceUnavailable, "database unreachable: "+err.Error())
		return
	}

	state, err := a.db.MigrationState(ctx)
	if err != nil {
		plain(w, http.StatusServiceUnavailable, "migration state unknown: "+err.Error())
		return
	}
	if !state.UpToDate() {
		plain(w, http.StatusServiceUnavailable, "schema is behind; migrations pending")
		return
	}

	plain(w, http.StatusOK, "ready")
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(body + "\n"))
}
