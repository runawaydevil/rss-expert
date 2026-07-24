package webmention

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T, domain string) (*Store, *identity.Account) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-wm")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	moderator, err := identity.NewStore(db).Create(ctx, "mod@"+domain, "a long enough password", identity.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	return New(db, Options{Domain: domain, AllowPrivateAddrs: true}), moderator
}

func TestATargetOnAnotherSiteIsRefused(t *testing.T) {
	s, _ := testStore(t, "ours.example")
	ctx := context.Background()

	if _, err := s.Receive(ctx, "https://them.example/p/1", "https://someone-else.example/p/2"); !errors.Is(err, ErrTargetNotOurs) {
		t.Errorf("a mention aimed at another site was accepted: %v", err)
	}
	if _, err := s.Receive(ctx, "https://a.example/1", "https://a.example/1"); !errors.Is(err, ErrSameURL) {
		t.Errorf("a page mentioning itself was accepted: %v", err)
	}
	for _, bad := range []string{"", "not a url", "javascript:alert(1)", "ftp://a.example/x"} {
		if _, err := s.Receive(ctx, bad, "https://ours.example/p/1"); err == nil {
			t.Errorf("source %q was accepted", bad)
		}
	}
}

func TestVerificationDemandsARealLink(t *testing.T) {
	s, _ := testStore(t, "ours.example")
	ctx := context.Background()

	var body string
	var status = http.StatusOK
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer source.Close()

	target := "https://ours.example/p/1"

	body = `<html><body><p>I never mentioned anyone.</p></body></html>`
	mention, err := s.Receive(ctx, source.URL, target)
	if err != nil {
		t.Fatal(err)
	}
	if mention.State != Pending {
		t.Fatalf("a fresh mention is %q, want pending", mention.State)
	}

	if err := s.VerifyIncoming(ctx, mention); !errors.Is(err, ErrNoLink) {
		t.Fatalf("a page that never links to the target verified: %v", err)
	}
	after, _ := s.ByID(ctx, mention.ID)
	if after.State != Failed {
		t.Errorf("state = %q after a failed check", after.State)
	}

	body = fmt.Sprintf(`<html><body>
		<div class="h-card"><a class="p-name u-url" href="/">Carol</a></div>
		<p>Replying to <a href="%s">that post</a>.</p>
	</body></html>`, target)
	if err := s.VerifyIncoming(ctx, after); err != nil {
		t.Fatalf("a page that does link failed to verify: %v", err)
	}

	verified, _ := s.ByID(ctx, mention.ID)
	if verified.State != Verified {
		t.Fatalf("state = %q, want verified", verified.State)
	}
	if verified.AuthorName != "Carol" {
		t.Errorf("the author was not read from the h-card: %q", verified.AuthorName)
	}
	if verified.LastError != "" {
		t.Errorf("the earlier failure was not cleared: %q", verified.LastError)
	}
}

func TestAWithdrawnSourceMarksTheMentionDeleted(t *testing.T) {
	s, _ := testStore(t, "ours.example")
	ctx := context.Background()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer source.Close()

	mention, err := s.Receive(ctx, source.URL, "https://ours.example/p/1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyIncoming(ctx, mention); err != nil {
		t.Fatal(err)
	}

	after, _ := s.ByID(ctx, mention.ID)
	if after.State != Deleted {
		t.Errorf("a source that answers 410 left the mention as %q, want deleted", after.State)
	}
}

func TestNothingIsShownBeforeAModeratorAgrees(t *testing.T) {
	s, moderator := testStore(t, "ours.example")
	ctx := context.Background()
	target := "https://ours.example/p/1"

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><a href="%s">there</a></body></html>`, target)
	}))
	defer source.Close()

	mention, err := s.Receive(ctx, source.URL, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyIncoming(ctx, mention); err != nil {
		t.Fatal(err)
	}

	shown, err := s.Approved(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 0 {
		t.Fatal("a verified but undecided mention is already on display")
	}

	waiting, err := s.AwaitingModeration(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 {
		t.Fatalf("%d mentions await a decision, want 1", len(waiting))
	}

	if err := s.Decide(ctx, mention.ID, moderator.ID, true); err != nil {
		t.Fatal(err)
	}
	shown, _ = s.Approved(ctx, target)
	if len(shown) != 1 {
		t.Errorf("%d mentions shown after approval", len(shown))
	}
	if waiting, _ := s.AwaitingModeration(ctx, 10); len(waiting) != 0 {
		t.Error("the decided mention is still in the queue")
	}

	if err := s.Decide(ctx, 9999, moderator.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("deciding a mention that does not exist gave %v", err)
	}
}

func TestRejectionKeepsItOffTheePage(t *testing.T) {
	s, moderator := testStore(t, "ours.example")
	ctx := context.Background()
	target := "https://ours.example/p/1"

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body><a href="%s">there</a></body></html>`, target)
	}))
	defer source.Close()

	mention, _ := s.Receive(ctx, source.URL, target)
	s.VerifyIncoming(ctx, mention)
	if err := s.Decide(ctx, mention.ID, moderator.ID, false); err != nil {
		t.Fatal(err)
	}

	if shown, _ := s.Approved(ctx, target); len(shown) != 0 {
		t.Error("a rejected mention is on display")
	}
}

func TestSendingDiscoversTheEndpointAndPostsToIt(t *testing.T) {
	s, _ := testStore(t, "ours.example")
	ctx := context.Background()

	var received url.Values
	var advertise func(w http.ResponseWriter)

	their := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/webmention" {
			r.ParseForm()
			received = r.PostForm
			w.WriteHeader(http.StatusAccepted)
			return
		}
		advertise(w)
	}))
	defer their.Close()

	target := their.URL + "/p/9"
	source := "https://ours.example/p/1"

	advertise = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>no endpoint here</body></html>`))
	}
	if _, err := s.Send(ctx, source, target); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("sending to a site with no endpoint gave %v", err)
	}

	advertise = func(w http.ResponseWriter) {
		w.Header().Set("Link", `</webmention>; rel="webmention"`)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>hi</body></html>`))
	}
	mention, err := s.Send(ctx, source, target)
	if err != nil {
		t.Fatalf("sending failed: %v", err)
	}
	if mention.State != Approved {
		t.Errorf("state = %q after a successful send", mention.State)
	}
	if received.Get("source") != source || received.Get("target") != target {
		t.Errorf("the endpoint received %v", received)
	}
	if !strings.HasSuffix(mention.Endpoint, "/webmention") {
		t.Errorf("endpoint = %q", mention.Endpoint)
	}

	journey, err := s.Journey(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(journey) != 1 || journey[0].State != Approved {
		t.Errorf("journey = %+v", journey)
	}
}

func TestEndpointFromLinkHeader(t *testing.T) {
	base, _ := url.Parse("https://them.example/p/9")

	for _, tc := range []struct {
		header string
		want   string
	}{
		{`</wm>; rel="webmention"`, "https://them.example/wm"},
		{`<https://wm.example/x>; rel=webmention`, "https://wm.example/x"},
		{`</a>; rel="other", </wm>; rel="webmention"`, "https://them.example/wm"},
		{`</wm>; rel="webmention somethingelse"`, "https://them.example/wm"},
		{`</wm>; rel="other"`, ""},
		{`nonsense`, ""},
		{`<javascript:alert(1)>; rel="webmention"`, ""},
	} {
		if got := endpointFromLinkHeader([]string{tc.header}, base); got != tc.want {
			t.Errorf("Link: %s gave %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestReceivingTheSameMentionTwiceUpdatesInPlace(t *testing.T) {
	s, _ := testStore(t, "ours.example")
	ctx := context.Background()

	first, err := s.Receive(ctx, "https://them.example/p/1", "https://ours.example/p/1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Receive(ctx, "https://them.example/p/1", "https://ours.example/p/1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("the same mention made two rows: %d and %d", first.ID, second.ID)
	}
}
