package web

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

func liveApp(t *testing.T) (*httptest.Server, *publish.Store, *identity.Account) {
	t.Helper()
	ctx := context.Background()

	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(nil)
	base := "http://" + server.Listener.Addr().String()
	server.Config.Handler = New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), base, Options{}).Handler()
	server.Start()
	t.Cleanup(server.Close)

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "owner@test.example", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	posts := publish.NewStore(db, base)
	if _, err := posts.EnsureHandle(ctx, owner); err != nil {
		t.Fatal(err)
	}
	return server, posts, owner
}

func openStream(t *testing.T, url string) (*bufio.Reader, func()) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q; nginx would buffer the stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("the stream was compressed (%q); events would sit in a buffer", got)
	}

	return bufio.NewReader(resp.Body), func() { resp.Body.Close() }
}

func TestTheStreamAnnouncesAFreshPost(t *testing.T) {
	server, posts, owner := liveApp(t)

	stream, done := openStream(t, server.URL+"/events?since=0")
	defer done()

	first, err := stream.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "retry:") {
		t.Errorf("the stream did not open with a retry hint: %q", first)
	}

	if _, err := posts.Create(context.Background(), owner, "Live", "Something new to see.", ""); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		line, err := stream.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream closed early: %v", err)
		}
		if strings.HasPrefix(line, "event: fresh") {
			return
		}
	}
	t.Fatal("no fresh event arrived within the deadline")
}

func TestTheStreamSaysNothingWhenNothingHappens(t *testing.T) {
	server, _, _ := liveApp(t)

	stream, done := openStream(t, server.URL+"/events?since=999999")
	defer done()

	if _, err := stream.ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	seen := make(chan string, 1)
	go func() {
		for {
			line, err := stream.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event:") {
				seen <- line
				return
			}
		}
	}()

	select {
	case line := <-seen:
		t.Errorf("the stream invented an event: %q", line)
	case <-time.After(6 * time.Second):
	}
}

func TestTheTimelineCarriesTheCursorAndTheIsland(t *testing.T) {
	server, posts, owner := liveApp(t)

	if _, err := posts.Create(context.Background(), owner, "Anything", "So the page has a cursor.", ""); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	if !strings.Contains(page, `data-latest="`) {
		t.Error("the page carries no cursor, so the island cannot ask about newer posts")
	}
	if !strings.Contains(page, `id="fresh"`) {
		t.Error("the banner the island reveals is missing")
	}
	if !strings.Contains(page, `src="/assets/live.js`) {
		t.Error("the island is not loaded")
	}
	if !strings.Contains(page, "hidden") {
		t.Error("the banner should start hidden")
	}
}

func TestFederationEndpointsAreThrottled(t *testing.T) {
	h := testApp(t)

	form := url.Values{"hub.mode": {"subscribe"}, "hub.topic": {"https://x.example/f.xml"},
		"hub.callback": {"https://x.example/cb"}}

	var limited bool
	for i := 0; i < 60; i++ {
		resp := postForm(t, h, "/websub/hub", form, nil)
		if resp.StatusCode == http.StatusTooManyRequests {
			if resp.Header.Get("Retry-After") == "" {
				t.Error("a throttled answer carries no Retry-After")
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Error("the hub took 60 requests from one address without ever refusing")
	}
}

func TestAccountPagesStayReachableWhenFederationIsFlooded(t *testing.T) {
	h := testApp(t)

	form := url.Values{"hub.mode": {"subscribe"}, "hub.topic": {"https://x.example/f.xml"},
		"hub.callback": {"https://x.example/cb"}}
	for i := 0; i < 60; i++ {
		postForm(t, h, "/websub/hub", form, nil)
	}

	if resp := get(t, h, "/login"); resp.StatusCode != http.StatusOK {
		t.Errorf("flooding the hub locked out the sign-in page: %d", resp.StatusCode)
	}
	if resp := get(t, h, "/"); resp.StatusCode != http.StatusOK {
		t.Errorf("flooding the hub locked out the timeline: %d", resp.StatusCode)
	}
}
