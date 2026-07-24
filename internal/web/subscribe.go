package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/runawaydevil/rss-expert/internal/feedin"
	"github.com/runawaydevil/rss-expert/internal/indieweb"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/safety"
)

var errNoFeedThere = errors.New("nothing at that address looks like a feed")

type candidate struct {
	URL   string
	Title string
	Kind  string
}

func (a *App) subscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.sourcesProblem(w, r, "That form expired. Try again.")
		return
	}

	raw := strings.TrimSpace(r.PostFormValue("url"))
	if raw == "" {
		a.sourcesProblem(w, r, "Give it an address first.")
		return
	}
	if !strings.Contains(raw, "//") {
		raw = "https://" + raw
	}

	found, err := a.findFeeds(ctx, raw)
	switch {
	case errors.Is(err, errNoFeedThere):
		a.sourcesProblem(w, r, "Nothing at that address looks like a feed, and the page does not point at one.")
		return
	case err != nil:
		a.log.Info("could not reach a source someone tried to add", "url", raw, "error", err)
		a.sourcesProblem(w, r, "That address could not be reached: "+err.Error())
		return
	}

	if len(found) > 1 {
		a.renderSources(w, r, "", "", found)
		return
	}

	source, err := a.sources.AddSource(ctx, found[0].URL)
	if err != nil {
		a.log.Error("could not add a source", "url", found[0].URL, "error", err)
		a.sourcesProblem(w, r, "It could not be added: "+err.Error())
		return
	}

	a.log.Info("source added", "id", source.ID, "url", source.FeedURL)
	a.readNow(ctx, source)
	a.AskForPush(ctx, source)
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

func (a *App) findFeeds(ctx context.Context, raw string) ([]candidate, error) {
	result, err := a.fetcher.Get(ctx, raw, nil)
	if err != nil {
		return nil, err
	}
	if result.StatusCode >= 400 {
		return nil, errors.New("the server answered " + strconv.Itoa(result.StatusCode))
	}

	if parsed, err := feedin.Parse(result.Body); err == nil {
		title := parsed.Title
		if title == "" {
			title = result.URL.Host
		}
		return []candidate{{URL: result.URL.String(), Title: title, Kind: "feed"}}, nil
	}

	page, err := indieweb.Discover(result.URL, result.Body)
	if err != nil {
		return nil, errNoFeedThere
	}

	var out []candidate
	for _, feed := range page.Feeds {
		if _, err := url.Parse(feed.URL); err != nil {
			continue
		}
		title := feed.Title
		if title == "" {
			title = feed.URL
		}
		out = append(out, candidate{URL: feed.URL, Title: title, Kind: feed.Type})
	}
	if len(out) == 0 {
		return nil, errNoFeedThere
	}
	return out, nil
}

func (a *App) readNow(ctx context.Context, source *ingest.Source) {
	if err := a.poller.Fetch(ctx, source); err != nil {
		a.log.Info("the first read of a new source did not work", "url", source.FeedURL, "error", err)
	}
}

func (a *App) refreshSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.sourcesProblem(w, r, "That form expired. Try again.")
		return
	}

	id, err := strconv.ParseInt(r.PostFormValue("source"), 10, 64)
	if err != nil {
		a.sourcesProblem(w, r, "No such source.")
		return
	}

	source, err := a.sources.SourceByID(ctx, id)
	if err != nil {
		a.sourcesProblem(w, r, "No such source.")
		return
	}

	a.readNow(ctx, source)
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

func (a *App) unsubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil || !validCSRF(r) {
		a.sourcesProblem(w, r, "That form expired. Try again.")
		return
	}

	id, err := strconv.ParseInt(r.PostFormValue("source"), 10, 64)
	if err != nil {
		a.sourcesProblem(w, r, "No such source.")
		return
	}
	if source, err := a.sources.SourceByID(ctx, id); err == nil {
		a.LeaveHub(ctx, source)
	}
	if err := a.sources.RemoveSource(ctx, id); err != nil {
		a.log.Error("could not remove a source", "id", id, "error", err)
		a.sourcesProblem(w, r, "It could not be removed: "+err.Error())
		return
	}

	a.log.Info("source removed", "id", id, "by", accountFrom(ctx).Email)
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

func (a *App) sourcesProblem(w http.ResponseWriter, r *http.Request, problem string) {
	a.renderSources(w, r, problem, r.PostFormValue("url"), nil)
}

func newFetcher(domain string, maxBytes int64, reachPrivate bool) *safety.Fetcher {
	return safety.New(safety.Options{
		UserAgent:         "rss-expert (+" + domain + ")",
		MaxBytes:          maxBytes,
		AllowPrivateAddrs: reachPrivate,
	})
}
