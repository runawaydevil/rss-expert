package web

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/indieweb"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/ledger"
	"github.com/runawaydevil/rss-expert/internal/media"
	"github.com/runawaydevil/rss-expert/internal/micropub"
	"github.com/runawaydevil/rss-expert/internal/moderation"
	"github.com/runawaydevil/rss-expert/internal/publish"
	"github.com/runawaydevil/rss-expert/internal/reading"
	"github.com/runawaydevil/rss-expert/internal/store"
	"github.com/runawaydevil/rss-expert/internal/webmention"
)

type App struct {
	db          *store.DB
	accounts    *identity.Store
	sources     *ingest.Store
	posts       *publish.Store
	sites       *indieweb.Store
	mentions    *webmention.Store
	tokens      *micropub.Store
	media       *media.Store
	reading     *reading.Store
	moderation  *moderation.Store
	queue       *jobs.Queue
	ledger      *ledger.Ledger
	auth        *auth
	log         *slog.Logger
	domain      string
	behindProxy bool
	showPreview bool
}

type Options struct {
	MediaRoot   string
	MediaQuota  int64
	BehindProxy bool
	ShowPreview bool
}

func NewApp(db *store.DB, log *slog.Logger, domain string) *App {
	return New(db, log, domain, Options{ShowPreview: true})
}

func New(db *store.DB, log *slog.Logger, domain string, o Options) *App {
	if o.MediaRoot == "" {
		o.MediaRoot = filepath.Join(filepath.Dir(db.Path), "media")
	}

	accounts := identity.NewStore(db)
	return &App{
		db:       db,
		accounts: accounts,
		sources:  ingest.NewStore(db),
		posts:    publish.NewStore(db, domain),
		sites: indieweb.NewStore(db, indieweb.Options{
			Domain:    domain,
			UserAgent: "rss-expert (+https://" + domain + ")",
		}),
		mentions: webmention.New(db, webmention.Options{
			Domain:    domain,
			UserAgent: "rss-expert (+https://" + domain + ")",
		}),
		tokens:      micropub.New(db),
		media:       media.New(db, media.Options{Root: o.MediaRoot, Quota: o.MediaQuota}),
		reading:     reading.New(db),
		moderation:  moderation.New(db),
		queue:       jobs.New(db),
		ledger:      ledger.New(db),
		auth:        newAuth(accounts, log, o.BehindProxy),
		log:         log,
		domain:      domain,
		behindProxy: o.BehindProxy,
		showPreview: o.ShowPreview,
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.timeline)
	mux.HandleFunc("GET /login", a.auth.login)
	mux.HandleFunc("POST /login", a.auth.submitLogin)
	mux.HandleFunc("POST /logout", a.auth.logout)
	if a.showPreview {
		mux.HandleFunc("GET /dev/preview", a.preview)
	}

	mux.HandleFunc("GET /users/rss.xml", a.firehoseFeed)
	mux.HandleFunc("GET /users/{handle}/rss.xml", a.accountFeed)
	mux.HandleFunc("GET /users/{handle}", a.profile)
	mux.HandleFunc("GET /settings/sites", a.auth.requireAccount(a.sitesPage))
	mux.HandleFunc("POST /settings/sites/claim", a.auth.requireAccount(a.claimSite))
	mux.HandleFunc("POST /settings/sites/verify", a.auth.requireAccount(a.verifySite))
	mux.HandleFunc("POST /settings/sites/release", a.auth.requireAccount(a.releaseSite))
	mux.HandleFunc("GET /p/{id}", a.postPage)
	mux.HandleFunc("GET /p/{id}/replies.xml", a.repliesFeed)
	mux.HandleFunc("GET /p/{id}/journey", a.journey)

	mux.HandleFunc("POST /webmention", a.receiveWebmention)
	mux.HandleFunc("GET /micropub", a.micropubEndpoint)
	mux.HandleFunc("POST /micropub", a.micropubEndpoint)

	mux.HandleFunc("GET /sources", a.sourcesPage)
	mux.HandleFunc("GET /subscriptions.opml", a.exportOPML)
	mux.HandleFunc("POST /subscriptions.opml", a.auth.requireAccount(a.importOPML))
	mux.HandleFunc("POST /mark", a.auth.requireAccount(a.mark))
	mux.HandleFunc("POST /collections", a.auth.requireAccount(a.collections))

	mux.HandleFunc("GET /rules", a.rules)
	mux.HandleFunc("GET /admin", a.requireModerator(a.adminPanel))
	mux.HandleFunc("POST /admin/report", a.requireModerator(a.decideReport))
	mux.HandleFunc("POST /admin/block", a.auth.requireAccount(a.blockSomething))
	mux.HandleFunc("POST /admin/job", a.requireModerator(a.requireFreshLogin(a.retryJob)))
	mux.HandleFunc("GET /admin/confirm", a.auth.requireAccount(a.confirmPage))
	mux.HandleFunc("POST /admin/confirm", a.auth.requireAccount(a.confirmIdentity))
	mux.HandleFunc("GET /settings/two-factor", a.auth.requireAccount(a.twoFactorPage))
	mux.HandleFunc("POST /settings/two-factor", a.auth.requireAccount(a.enableTwoFactor))
	mux.HandleFunc("POST /settings/two-factor/off", a.auth.requireAccount(a.disableTwoFactor))

	mux.HandleFunc("GET /media/{sha}", a.serveMedia)
	mux.HandleFunc("POST /micropub/media", a.micropubMedia)
	mux.HandleFunc("GET /settings/tokens", a.auth.requireAccount(a.tokenSettings))
	mux.HandleFunc("POST /settings/tokens", a.auth.requireAccount(a.issueToken))
	mux.HandleFunc("POST /settings/tokens/revoke", a.auth.requireAccount(a.revokeToken))
	mux.HandleFunc("GET /settings/media", a.auth.requireAccount(a.mediaLibrary))
	mux.HandleFunc("POST /settings/media", a.auth.requireAccount(a.uploadMedia))

	mux.HandleFunc("GET /write", a.auth.requireAccount(a.writeForm))
	mux.HandleFunc("POST /write", a.auth.requireAccount(a.submitWrite))

	mux.Handle("GET /assets/", assetHandler())

	return securityHeaders(compressed(a.auth.resolve(mux)))
}

const timelinePage = 40

func (a *App) rules(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "rules.html", map[string]any{
		"Title": "How this instance works — RSS Expert",
	})
}

func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "reader.html", map[string]any{
		"Title": "Preview — RSS Expert",
		"Posts": samplePosts(),
	})
}

func (a *App) host() string {
	if _, after, found := strings.Cut(a.domain, "//"); found {
		return after
	}
	return a.domain
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	data["Account"] = accountFrom(r.Context())
	data["CSRF"] = csrfToken(w, r, a.behindProxy)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render failed", "template", name, "error", err)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'none'; img-src 'self' data:; style-src 'self'; font-src 'self'; "+
				"media-src 'self'; "+
				"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
