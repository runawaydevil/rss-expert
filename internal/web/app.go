package web

import (
	"log/slog"
	"net/http"

	"github.com/runawaydevil/rss-social/internal/store"
)

type App struct {
	db     *store.DB
	log    *slog.Logger
	domain string
}

func NewApp(db *store.DB, log *slog.Logger, domain string) *App {
	return &App{db: db, log: log, domain: domain}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.root)
	return securityHeaders(mux)
}

func (a *App) root(w http.ResponseWriter, r *http.Request) {
	plain(w, http.StatusServiceUnavailable,
		"rss-social on "+a.domain+" is not serving yet: this build has a schema and a health check, and nothing else")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
