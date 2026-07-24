package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
)

const oneFeed = `<?xml version="1.0"?>
<rss version="2.0"><channel>
	<title>A small site</title>
	<link>https://small.example/</link>
	<description>Posts</description>
	<item><title>First</title><link>https://small.example/1</link>
	<guid isPermaLink="true">https://small.example/1</guid></item>
</channel></rss>`

func siteServing(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".xml") {
			w.Header().Set("Content-Type", "application/rss+xml")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func readerApp(t *testing.T) (http.Handler, *ingest.Store, *http.Cookie) {
	t.Helper()
	ctx := context.Background()

	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	app := New(db, quietLogger(), "test.example", Options{ReachPrivate: true})
	handler := app.Handler()
	session := signedInAs(t, handler, identity.NewStore(db), "alice@example.org", identity.RoleUser)

	return handler, ingest.NewStore(db), session
}

func follow(t *testing.T, h http.Handler, session *http.Cookie, address string) *http.Response {
	t.Helper()

	page := getAs(t, h, "/sources", session)
	body, _ := io.ReadAll(page.Body)
	m := csrfInput.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf field on the sources page:\n%s", body)
	}

	return postForm(t, h, "/sources", url.Values{
		"csrf": {string(m[1])},
		"url":  {address},
	}, append(page.Cookies(), session))
}

func TestFollowingAFeedFromTheBrowser(t *testing.T) {
	h, sources, session := readerApp(t)
	site := siteServing(t, map[string]string{"/rss.xml": oneFeed})

	resp := follow(t, h, session, site.URL+"/rss.xml")
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("following gave %d: %s", resp.StatusCode, body)
	}

	list, err := sources.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d sources after following one", len(list))
	}
	if list[0].Title != "A small site" {
		t.Errorf("title = %q; the feed was not read straight away", list[0].Title)
	}
}

func TestFollowingASiteFindsItsFeed(t *testing.T) {
	h, sources, session := readerApp(t)

	site := siteServing(t, map[string]string{"/rss.xml": oneFeed})
	site2 := siteServing(t, map[string]string{
		"/": `<!doctype html><html><head>
			<link rel="alternate" type="application/rss+xml" title="Posts" href="` + site.URL + `/rss.xml">
			</head><body><p>A site, not a feed.</p></body></html>`,
	})

	if resp := follow(t, h, session, site2.URL+"/"); resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("following a site gave %d: %s", resp.StatusCode, body)
	}

	list, err := sources.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d sources; the feed behind the page was not found", len(list))
	}
	if list[0].FeedURL != site.URL+"/rss.xml" {
		t.Errorf("feed url = %q", list[0].FeedURL)
	}
}

func TestASiteWithSeveralFeedsAsksWhich(t *testing.T) {
	h, sources, session := readerApp(t)

	site := siteServing(t, map[string]string{
		"/": `<!doctype html><html><head>
			<link rel="alternate" type="application/rss+xml" title="Everything" href="/all.xml">
			<link rel="alternate" type="application/atom+xml" title="Only the notes" href="/notes.xml">
			</head><body></body></html>`,
	})

	resp := follow(t, h, session, site.URL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a page with two feeds gave %d, want the page back with a choice", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Everything", "Only the notes"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the choice does not offer %q", want)
		}
	}

	list, _ := sources.Sources(context.Background())
	if len(list) != 0 {
		t.Errorf("%d sources were added without anyone choosing", len(list))
	}
}

func TestAddressWithNoFeedSaysSo(t *testing.T) {
	h, sources, session := readerApp(t)
	site := siteServing(t, map[string]string{"/": "<!doctype html><html><body>Just a page.</body></html>"})

	resp := follow(t, h, session, site.URL+"/")
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "does not point at one") {
		t.Errorf("no explanation was shown:\n%s", body)
	}

	list, _ := sources.Sources(context.Background())
	if len(list) != 0 {
		t.Errorf("%d sources added from a page with no feed", len(list))
	}
}

func TestOnlySignedInPeopleChangeTheSourceList(t *testing.T) {
	h, _, _ := readerApp(t)

	for _, path := range []string{"/sources", "/sources/refresh", "/sources/remove"} {
		resp := postForm(t, h, path, url.Values{"url": {"https://example.org/"}}, nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("%s from a signed-out visitor = %d", path, resp.StatusCode)
			continue
		}
		if location := resp.Header.Get("Location"); !strings.Contains(location, "/login") {
			t.Errorf("%s sent a stranger to %q instead of the sign-in page", path, location)
		}
	}
}

func TestUnfollowingRemovesIt(t *testing.T) {
	h, sources, session := readerApp(t)
	site := siteServing(t, map[string]string{"/rss.xml": oneFeed})

	follow(t, h, session, site.URL+"/rss.xml")

	list, _ := sources.Sources(context.Background())
	if len(list) != 1 {
		t.Fatalf("%d sources before unfollowing", len(list))
	}

	page := getAs(t, h, "/sources", session)
	body, _ := io.ReadAll(page.Body)
	m := csrfInput.FindSubmatch(body)

	resp := postForm(t, h, "/sources/remove", url.Values{
		"csrf":   {string(m[1])},
		"source": {strconv.FormatInt(list[0].ID, 10)},
	}, append(page.Cookies(), session))
	if resp.StatusCode != http.StatusSeeOther {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("unfollowing gave %d: %s", resp.StatusCode, out)
	}

	list, _ = sources.Sources(context.Background())
	if len(list) != 0 {
		t.Errorf("%d sources are still followed", len(list))
	}
}

func TestThePreviewKeepsTheDraftWithoutPublishing(t *testing.T) {
	h, _, session := readerApp(t)

	page := getAs(t, h, "/write", session)
	body, _ := io.ReadAll(page.Body)
	m := csrfInput.FindSubmatch(body)

	resp := postForm(t, h, "/write", url.Values{
		"csrf":     {string(m[1])},
		"title":    {"Half written"},
		"markdown": {"A **bold** start."},
		"preview":  {"1"},
	}, append(page.Cookies(), session))

	rendered, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rendered), "<strong>bold</strong>") {
		t.Errorf("the preview did not render the markdown:\n%s", rendered)
	}
	if !strings.Contains(string(rendered), "How it will read") {
		t.Error("the preview section is missing")
	}

	back, _ := io.ReadAll(getAs(t, h, "/write", session).Body)
	if !strings.Contains(string(back), "A **bold** start.") {
		t.Error("the draft was not there when the writer came back")
	}
	if !strings.Contains(string(back), "Half written") {
		t.Error("the title was not kept")
	}
}

func TestPublishingClearsTheDraft(t *testing.T) {
	h, _, session := readerApp(t)

	page := getAs(t, h, "/write", session)
	body, _ := io.ReadAll(page.Body)
	m := csrfInput.FindSubmatch(body)

	postForm(t, h, "/write", url.Values{
		"csrf":     {string(m[1])},
		"markdown": {"Kept for later."},
		"preview":  {"1"},
	}, append(page.Cookies(), session))

	page = getAs(t, h, "/write", session)
	body, _ = io.ReadAll(page.Body)
	m = csrfInput.FindSubmatch(body)

	resp := postForm(t, h, "/write", url.Values{
		"csrf":     {string(m[1])},
		"markdown": {"Kept for later."},
	}, append(page.Cookies(), session))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("publishing gave %d", resp.StatusCode)
	}

	back, _ := io.ReadAll(getAs(t, h, "/write", session).Body)
	if strings.Contains(string(back), "Kept for later.") {
		t.Error("the draft survived publication")
	}
}
