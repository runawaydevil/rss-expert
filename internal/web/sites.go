package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/runawaydevil/rss-expert/internal/indieweb"
)

func (a *App) profile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	handle := r.PathValue("handle")

	w.Header().Set("Vary", "Accept")
	if a.federates && wantsActivityJSON(r) {
		a.actorJSON(w, r)
		return
	}

	posts, err := a.posts.ByHandle(ctx, handle, 40)
	if err != nil {
		a.log.Error("could not read an account's posts", "error", err)
	}

	data := map[string]any{
		"Title":   handle + " — RSS Expert",
		"Handle":  handle,
		"Posts":   localViews(posts),
		"FeedURL": "/users/" + handle + "/rss.xml",
	}

	if len(posts) > 0 {
		sites, err := a.sites.ForAccount(ctx, posts[0].AccountID)
		if err != nil {
			a.log.Error("could not read sites", "error", err)
		}
		data["Sites"] = sites
		for _, site := range sites {
			if site.Verified() {
				data["Verified"] = site
				break
			}
		}
	}
	a.render(w, r, "profile.html", data)
}

func (a *App) sitesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	sites, err := a.sites.ForAccount(ctx, account.ID)
	if err != nil {
		a.log.Error("could not read sites", "error", err)
	}

	handle, err := a.posts.EnsureHandle(ctx, account)
	if err != nil {
		a.log.Error("could not resolve a handle", "error", err)
	}

	a.render(w, r, "sites.html", map[string]any{
		"Title":      "Your sites — RSS Expert",
		"Sites":      sites,
		"Handle":     handle,
		"ProfileURL": a.sites.ProfileURL(handle),
		"Problem":    r.URL.Query().Get("problem"),
	})
}

func (a *App) claimSite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	site, err := a.sites.Claim(ctx, account.ID, r.PostFormValue("url"))
	if err != nil {
		a.redirectWithProblem(w, r, err)
		return
	}

	a.moderation.Log(ctx, account, "site.claim", site.Host, "")
	http.Redirect(w, r, "/settings/sites", http.StatusSeeOther)
}

func (a *App) verifySite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(r.PostFormValue("site"), 10, 64)
	if err != nil {
		http.Error(w, "which site?", http.StatusBadRequest)
		return
	}

	site, err := a.sites.ByID(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.AccountID != account.ID {
		http.Error(w, "that site belongs to another account", http.StatusForbidden)
		return
	}

	handle, err := a.posts.EnsureHandle(ctx, account)
	if err != nil {
		a.log.Error("could not resolve a handle", "error", err)
	}

	if err := a.sites.Verify(ctx, site, handle); err != nil {
		a.log.Info("site verification failed", "host", site.Host, "error", err)
		a.redirectWithProblem(w, r, err)
		return
	}

	a.moderation.Log(ctx, account, "site.verify", site.Host, "")
	a.AnnounceProfile(context.WithoutCancel(ctx), account.ID, handle)
	http.Redirect(w, r, "/settings/sites", http.StatusSeeOther)
}

func (a *App) releaseSite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(r.PostFormValue("site"), 10, 64)
	if err != nil {
		http.Error(w, "which site?", http.StatusBadRequest)
		return
	}

	if err := a.sites.Release(ctx, account.ID, id); err != nil {
		if errors.Is(err, indieweb.ErrNotYours) {
			http.Error(w, "that site belongs to another account", http.StatusForbidden)
			return
		}
		a.log.Error("could not release a site", "error", err)
	}
	a.moderation.Log(ctx, account, "site.release", strconv.FormatInt(id, 10), "")
	if handle, err := a.posts.HandleFor(ctx, account.ID); err == nil && handle != "" {
		a.AnnounceProfile(context.WithoutCancel(ctx), account.ID, handle)
	}
	http.Redirect(w, r, "/settings/sites", http.StatusSeeOther)
}

func (a *App) redirectWithProblem(w http.ResponseWriter, r *http.Request, cause error) {
	http.Redirect(w, r, "/settings/sites?problem="+url.QueryEscape(cause.Error()), http.StatusSeeOther)
}
