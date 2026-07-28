package web

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/indieweb"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/ledger"
	"github.com/runawaydevil/rss-expert/internal/mail"
	"github.com/runawaydevil/rss-expert/internal/media"
	"github.com/runawaydevil/rss-expert/internal/micropub"
	"github.com/runawaydevil/rss-expert/internal/moderation"
	"github.com/runawaydevil/rss-expert/internal/poller"
	"github.com/runawaydevil/rss-expert/internal/publish"
	"github.com/runawaydevil/rss-expert/internal/push"
	"github.com/runawaydevil/rss-expert/internal/reading"
	"github.com/runawaydevil/rss-expert/internal/safety"
	"github.com/runawaydevil/rss-expert/internal/store"
	"github.com/runawaydevil/rss-expert/internal/webmention"
)

type App struct {
	db           *store.DB
	accounts     *identity.Store
	sources      *ingest.Store
	posts        *publish.Store
	sites        *indieweb.Store
	mentions     *webmention.Store
	tokens       *micropub.Store
	media        *media.Store
	reading      *reading.Store
	moderation   *moderation.Store
	queue        *jobs.Queue
	ledger       *ledger.Ledger
	auth         *auth
	ops          *ops
	fetcher      *safety.Fetcher
	push         *push.Store
	mail         *mail.Sender
	live         *liveGate
	federation   *limiter
	inboxLimit   *limiter
	ap           *activitypub.Store
	apClient     *activitypub.Client
	poller       *poller.Poller
	log          *slog.Logger
	domain       string
	behindProxy  bool
	showPreview  bool
	dataDir      string
	registration string
	federates    bool
}

type Options struct {
	MediaRoot    string
	MediaQuota   int64
	BehindProxy  bool
	ShowPreview  bool
	Version      string
	MetricsToken string
	FetchLimit   int64
	ReachPrivate bool
	DataDir      string
	Registration string
	ActivityPub  bool
	Mailer       *mail.Sender
}

func NewApp(db *store.DB, log *slog.Logger, domain string) *App {
	return New(db, log, domain, Options{ShowPreview: true})
}

func New(db *store.DB, log *slog.Logger, domain string, o Options) *App {
	if o.MediaRoot == "" {
		o.MediaRoot = filepath.Join(filepath.Dir(db.Path), "media")
	}
	if o.Version == "" {
		o.Version = "0.0.1"
	}

	accounts := identity.NewStore(db)
	sources := ingest.NewStore(db)

	return &App{
		db:       db,
		accounts: accounts,
		sources:  sources,
		posts:    publish.NewStore(db, domain),
		sites: indieweb.NewStore(db, indieweb.Options{
			Domain:    domain,
			UserAgent: "rss-expert (+https://" + domain + ")",
		}),
		mentions: webmention.New(db, webmention.Options{
			Domain:    domain,
			UserAgent: "rss-expert (+https://" + domain + ")",
		}),
		tokens:     micropub.New(db),
		media:      media.New(db, media.Options{Root: o.MediaRoot, Quota: o.MediaQuota}),
		reading:    reading.New(db),
		moderation: moderation.New(db),
		queue:      jobs.New(db),
		ledger:     ledger.New(db),
		auth:       newAuth(accounts, log, o.BehindProxy),
		ops:        newOps(db, o.Version, o.MetricsToken),
		fetcher:    newFetcher(domain, o.FetchLimit, o.ReachPrivate),
		push:       push.New(db),
		mail:       o.Mailer,
		live:       newLiveGate(liveMaxClients),
		federation: newLimiter(30, 120),
		inboxLimit: newLimiter(240, 3600),
		ap:         activitypub.New(db),
		apClient: activitypub.NewClient(activitypub.ClientOptions{
			UserAgent:    "rss-expert (+https://" + domain + ")",
			ReachPrivate: o.ReachPrivate,
		}),
		poller: poller.New(sources, log, poller.Options{
			UserAgent:         "rss-expert (+" + domain + ")",
			AllowPrivateAddrs: o.ReachPrivate,
			MaxBytes:          o.FetchLimit,
		}),
		log:          log,
		domain:       domain,
		behindProxy:  o.BehindProxy,
		showPreview:  o.ShowPreview,
		dataDir:      o.DataDir,
		registration: registrationMode(o.Registration),
		federates:    o.ActivityPub,
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.timeline)
	mux.HandleFunc("GET /events", a.events)
	mux.HandleFunc("GET /login", a.auth.login)
	mux.HandleFunc("POST /login", a.auth.submitLogin)
	mux.HandleFunc("POST /logout", a.auth.logout)

	mux.HandleFunc("GET /register", a.registerForm)
	mux.HandleFunc("POST /register", a.submitRegister)
	mux.HandleFunc("GET /account/verify", a.verifyEmail)
	mux.HandleFunc("GET /account/forgot", a.magicForm)
	mux.HandleFunc("POST /account/forgot", a.submitMagic)
	mux.HandleFunc("GET /account/link", a.followMagic)
	mux.HandleFunc("GET /account/recover", a.recoverForm)
	mux.HandleFunc("POST /account/recover", a.submitRecover)
	if a.showPreview {
		mux.HandleFunc("GET /dev/preview", a.preview)
	}

	if a.federates {
		mux.HandleFunc("GET /.well-known/webfinger", a.throttle(a.webfinger))
		mux.HandleFunc("POST /users/{handle}/inbox", a.inbox)
		mux.HandleFunc("POST /inbox", a.sharedInbox)
		mux.HandleFunc("GET /users/{handle}/followers", a.throttle(a.followersCollection))
		mux.HandleFunc("GET /users/{handle}/following", a.throttle(a.followingCollection))
		mux.HandleFunc("GET /users/{handle}/outbox", a.throttle(a.outboxCollection))
	}
	mux.HandleFunc("GET /users/rss.xml", a.firehoseFeed)
	mux.HandleFunc("GET /users/{handle}/rss.xml", a.accountFeed)
	mux.HandleFunc("GET /users/{handle}", a.profile)
	mux.HandleFunc("GET /settings", a.auth.requireAccount(a.settingsPage))
	mux.HandleFunc("GET /settings/sites", a.auth.requireAccount(a.sitesPage))
	mux.HandleFunc("POST /settings/sites/claim", a.auth.requireAccount(a.claimSite))
	mux.HandleFunc("POST /settings/sites/verify", a.auth.requireAccount(a.verifySite))
	mux.HandleFunc("POST /settings/sites/release", a.auth.requireAccount(a.releaseSite))
	mux.HandleFunc("GET /p/{id}", a.postPage)
	mux.HandleFunc("GET /p/{id}/replies.xml", a.repliesFeed)
	mux.HandleFunc("GET /p/{id}/journey", a.journey)
	mux.HandleFunc("GET /p/{id}/edit", a.auth.requireAccount(a.editForm))
	mux.HandleFunc("POST /p/{id}/edit", a.auth.requireAccount(a.submitEdit))
	mux.HandleFunc("POST /p/{id}/withdraw", a.auth.requireAccount(a.withdrawPost))

	mux.HandleFunc("POST /webmention", a.throttle(a.receiveWebmention))

	mux.HandleFunc("GET /websub/{source}", a.websubCallback)
	mux.HandleFunc("POST /websub/{source}", a.websubCallback)
	mux.HandleFunc("POST /websub/hub", a.throttle(a.hubEndpoint))
	mux.HandleFunc("GET /rsscloud/{source}", a.cloudCallback)
	mux.HandleFunc("POST /rsscloud/{source}", a.cloudCallback)
	mux.HandleFunc("POST /rsscloud/pleaseNotify", a.throttle(a.cloudRegister))
	mux.HandleFunc("GET /micropub", a.micropubEndpoint)
	mux.HandleFunc("POST /micropub", a.throttle(a.micropubEndpoint))

	mux.HandleFunc("GET /sources", a.sourcesPage)
	mux.HandleFunc("POST /sources", a.auth.requireAccount(a.subscribe))
	mux.HandleFunc("POST /sources/refresh", a.auth.requireAccount(a.refreshSource))
	mux.HandleFunc("POST /sources/remove", a.auth.requireAccount(a.unsubscribe))
	mux.HandleFunc("GET /subscriptions.opml", a.exportOPML)
	mux.HandleFunc("POST /subscriptions.opml", a.auth.requireAccount(a.importOPML))
	mux.HandleFunc("POST /mark", a.auth.requireAccount(a.mark))
	mux.HandleFunc("POST /collections", a.auth.requireAccount(a.collections))

	mux.HandleFunc("GET /healthz", a.ops.healthz)
	mux.HandleFunc("GET /readyz", a.ops.readyz)
	mux.HandleFunc("GET /metrics", a.ops.metrics)
	mux.HandleFunc("GET /debug/heap", a.ops.heapProfile)

	mux.HandleFunc("GET /rules", a.rules)
	mux.HandleFunc("GET /admin/status", a.requireModerator(a.statusPage))
	mux.HandleFunc("POST /admin/backup", a.requireModerator(a.takeBackup))
	mux.HandleFunc("POST /admin/invite", a.requireModerator(a.inviteForm))
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
	mux.HandleFunc("POST /micropub/media", a.throttle(a.micropubMedia))
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

func registrationMode(mode string) string {
	switch mode {
	case "invite", "open":
		return mode
	default:
		return "closed"
	}
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
				"media-src 'self'; script-src 'self'; connect-src 'self'; "+
				"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
