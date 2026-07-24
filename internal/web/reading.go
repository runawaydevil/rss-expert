package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/opml"
)

func (a *App) timeline(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())
	ctx := r.Context()

	query := ingest.Query{Limit: timelinePage}
	if account != nil {
		query.AccountID = account.ID
	}
	if raw := r.URL.Query().Get("before"); raw != "" {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			query.Before = time.Unix(unix, 0).UTC()
		}
	}

	name := r.URL.Query().Get("view")
	label := "Everything"

	switch name {
	case "unread":
		query.UnreadOnly, label = true, "Unread"
	case "saved":
		query.SavedOnly, label = true, "Saved"
	case "conversations":
		query.Threaded, label = true, "Conversations"
	}

	scope := ingest.Scope(r.URL.Query().Get("scope"))
	switch scope {
	case ingest.Here:
		query.Scope, label = scope, "Written here"
	case ingest.Elsewhere:
		query.Scope, label = scope, "From elsewhere"
	case ingest.Mine:
		query.Scope = scope
		label = "Yours"
		if account != nil {
			if handle, err := a.posts.HandleFor(ctx, account.ID); err == nil {
				query.FeedURL = a.posts.AccountFeedURL(handle)
			}
		}
	default:
		scope = ingest.Everything
	}

	if raw := r.URL.Query().Get("collection"); raw != "" && account != nil {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if ids, err := a.reading.CollectionSources(ctx, account.ID, id); err == nil {
				query.SourceIDs = ids
				label = "A collection"
			}
		}
	}

	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if search != "" {
		hits, err := a.reading.Search(ctx, search, timelinePage)
		if err != nil {
			a.log.Error("search failed", "error", err)
		}
		query.Keys = make([]string, 0, len(hits))
		for _, hit := range hits {
			query.Keys = append(query.Keys, hit.Key)
		}
		label = fmt.Sprintf("%d results for %q", len(hits), search)
		if len(query.Keys) == 0 {
			query.Keys = []string{"\x00 nothing matches"}
		}
	}

	items, err := a.sources.Select(ctx, query)
	if err != nil {
		a.log.Error("could not read the timeline", "error", err)
	}

	latest, err := a.sources.Newest(ctx)
	if err != nil {
		a.log.Error("could not read the newest item time", "error", err)
	}

	older := ""
	if len(items) == timelinePage {
		oldest := items[len(items)-1]
		when := oldest.Published
		if when.IsZero() {
			when = oldest.Updated
		}
		if !when.IsZero() {
			older = moreLink(r.URL, when)
		}
	}

	data := map[string]any{
		"Title":  "RSS Expert",
		"Posts":  a.decorate(ctx, account, items),
		"View":   name,
		"Latest": latest,
		"Older":  older,
		"Scope":  string(scope),
		"Label":  label,
		"Search": search,
	}
	if account != nil {
		if unread, err := a.reading.UnreadCount(ctx, account.ID); err == nil {
			data["Unread"] = unread
		}
		if saved, err := a.reading.SavedCount(ctx, account.ID); err == nil {
			data["Saved"] = saved
		}
		if collections, err := a.reading.Collections(ctx, account.ID); err == nil {
			data["Collections"] = collections
		}
	}
	a.render(w, r, "reader.html", data)
}

func (a *App) decorate(ctx context.Context, account *identity.Account, items []ingest.Item) []postView {
	views := timelineViews(items)
	if account == nil {
		return views
	}

	keys := make([]string, 0, len(items))
	for i := range items {
		keys = append(keys, items[i].Key)
	}
	flags, err := a.reading.FlagsFor(ctx, account.ID, keys)
	if err != nil {
		return views
	}
	for i := range views {
		f := flags[items[i].Key]
		views[i].Read = f.Read
		views[i].Saved = f.Saved
		views[i].Key = items[i].Key
	}
	return views
}

func (a *App) mark(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	keys := r.PostForm["key"]
	var err error
	switch r.PostFormValue("action") {
	case "read":
		err = a.reading.MarkRead(r.Context(), account.ID, keys...)
	case "unread":
		err = a.reading.MarkUnread(r.Context(), account.ID, keys...)
	case "save":
		err = a.reading.Save(r.Context(), account.ID, keys...)
	case "unsave":
		err = a.reading.Unsave(r.Context(), account.ID, keys...)
	case "read-all":
		_, err = a.reading.MarkAllRead(r.Context(), account.ID, time.Now().UTC())
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		a.log.Error("could not update reading state", "error", err)
	}

	back := r.PostFormValue("back")
	if back == "" || !strings.HasPrefix(back, "/") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (a *App) sourcesPage(w http.ResponseWriter, r *http.Request) {
	a.renderSources(w, r, "", "", nil)
}

func (a *App) renderSources(w http.ResponseWriter, r *http.Request, problem, draft string, choices []candidate) {
	ctx := r.Context()

	list, err := a.sources.Sources(ctx)
	if err != nil {
		a.log.Error("could not list sources", "error", err)
	}

	ids := make([]int64, 0, len(list))
	for _, source := range list {
		ids = append(ids, source.ID)
	}
	health, err := a.sources.HealthFor(ctx, ids)
	if err != nil {
		a.log.Error("could not read source health", "error", err)
	}

	byID := map[int64]ingest.Health{}
	for _, h := range health {
		byID[h.SourceID] = h
	}

	rows := make([]map[string]any, 0, len(list))
	for _, source := range list {
		h := byID[source.ID]
		rows = append(rows, map[string]any{
			"ID":       source.ID,
			"Title":    source.Title,
			"FeedURL":  source.FeedURL,
			"SiteURL":  source.SiteURL,
			"State":    h.State,
			"Detail":   h.Detail,
			"Action":   h.Action,
			"LastRead": age(source.LastFetchAt),
			"Every":    source.PollInterval.String(),
		})
	}

	a.render(w, r, "sources.html", map[string]any{
		"Title":   "Sources — RSS Expert",
		"Sources": rows,
		"Problem": problem,
		"Draft":   draft,
		"Choices": choices,
	})
}

func (a *App) exportOPML(w http.ResponseWriter, r *http.Request) {
	list, err := a.sources.Sources(r.Context())
	if err != nil {
		http.Error(w, "could not read the sources", http.StatusInternalServerError)
		return
	}

	subscriptions := make([]opml.Subscription, 0, len(list))
	for _, source := range list {
		subscriptions = append(subscriptions, opml.Subscription{
			Title:   source.Title,
			FeedURL: source.FeedURL,
			SiteURL: source.SiteURL,
		})
	}

	host := a.host()
	body, err := opml.Render("Subscriptions on "+host, host, subscriptions, time.Now())
	if err != nil {
		http.Error(w, "could not build the outline", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="subscriptions.opml"`)
	w.Write(body)
}

func (a *App) importOPML(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())
	if !account.Role.CanAdminister() {
		http.Error(w, "only an administrator can import subscriptions", http.StatusForbidden)
		return
	}

	if err := r.ParseMultipartForm(4 << 20); err != nil {
		http.Error(w, "that upload could not be read", http.StatusBadRequest)
		return
	}
	if !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("outline")
	if err != nil {
		http.Error(w, "no outline was attached", http.StatusBadRequest)
		return
	}
	defer file.Close()

	body := make([]byte, 4<<20)
	n, _ := file.Read(body)

	subscriptions, err := opml.Parse(body[:n])
	if err != nil {
		http.Error(w, "that file is not an OPML outline", http.StatusBadRequest)
		return
	}

	added := 0
	for _, sub := range subscriptions {
		if _, err := a.sources.AddSource(r.Context(), sub.FeedURL); err != nil {
			a.log.Warn("could not add a source from the outline", "feed", sub.FeedURL, "error", err)
			continue
		}
		added++
	}

	a.log.Info("imported an outline", "found", len(subscriptions), "added", added, "by", account.Email)
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

func (a *App) collections(w http.ResponseWriter, r *http.Request) {
	account := accountFrom(r.Context())

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		http.Error(w, "that form expired", http.StatusBadRequest)
		return
	}

	switch r.PostFormValue("action") {
	case "create":
		if _, err := a.reading.CreateCollection(r.Context(), account.ID, r.PostFormValue("name")); err != nil {
			a.log.Warn("could not create a collection", "error", err)
		}
	case "add", "remove":
		collectionID, _ := strconv.ParseInt(r.PostFormValue("collection"), 10, 64)
		sourceID, _ := strconv.ParseInt(r.PostFormValue("source"), 10, 64)
		var err error
		if r.PostFormValue("action") == "add" {
			err = a.reading.AddToCollection(r.Context(), account.ID, collectionID, sourceID)
		} else {
			err = a.reading.RemoveFromCollection(r.Context(), account.ID, collectionID, sourceID)
		}
		if err != nil {
			a.log.Warn("could not change a collection", "error", err)
		}
	case "delete":
		id, _ := strconv.ParseInt(r.PostFormValue("collection"), 10, 64)
		if err := a.reading.DeleteCollection(r.Context(), account.ID, id); err != nil {
			a.log.Warn("could not delete a collection", "error", err)
		}
	}
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

func moreLink(current *url.URL, before time.Time) string {
	next := url.Values{}
	for _, keep := range []string{"view", "scope", "q", "collection"} {
		if v := current.Query().Get(keep); v != "" {
			next.Set(keep, v)
		}
	}
	next.Set("before", strconv.FormatInt(before.Unix(), 10))
	return "/?" + next.Encode()
}

func (a *App) settingsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	twoFactor, err := a.accounts.TOTPEnabled(ctx, account.ID)
	if err != nil {
		a.log.Error("could not read two-factor state", "error", err)
	}

	feedPath := "/users/rss.xml"
	if handle, err := a.posts.HandleFor(ctx, account.ID); err == nil {
		feedPath = "/users/" + handle + "/rss.xml"
	}

	a.render(w, r, "settings.html", map[string]any{
		"Title":     "Settings — RSS Expert",
		"TwoFactor": twoFactor,
		"FeedPath":  feedPath,
	})
}
