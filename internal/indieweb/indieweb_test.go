package indieweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestDiscoverReadsAnHCardAndRelMe(t *testing.T) {
	page, err := Discover(mustURL(t, "https://alice.example/"), []byte(`
<!doctype html>
<html><head>
  <link rel="me" href="https://social.example/users/alice">
  <link rel="alternate" type="application/rss+xml" title="Posts" href="/rss.xml">
  <link rel="alternate" type="application/atom+xml" href="/atom.xml">
  <link rel="authorization_endpoint" href="https://alice.example/auth">
  <link rel="micropub" href="/micropub">
</head><body>
  <div class="h-card">
    <img class="u-photo" src="/me.jpg" alt="">
    <a class="p-name u-url" href="/">Alice   Example</a>
    <p class="p-note">Writes about feeds.</p>
  </div>
  <a rel="me" href="https://github.example/alice">github</a>
</body></html>`))
	if err != nil {
		t.Fatal(err)
	}

	if page.Card.Name != "Alice Example" {
		t.Errorf("name = %q, want the whitespace collapsed", page.Card.Name)
	}
	if page.Card.Photo != "https://alice.example/me.jpg" {
		t.Errorf("photo = %q, want it resolved against the page", page.Card.Photo)
	}
	if page.Card.Note != "Writes about feeds." {
		t.Errorf("note = %q", page.Card.Note)
	}

	if len(page.RelMe) != 2 {
		t.Fatalf("rel=me = %v", page.RelMe)
	}
	if page.RelMe[0] != "https://social.example/users/alice" {
		t.Errorf("first rel=me = %q", page.RelMe[0])
	}

	if len(page.Feeds) != 2 {
		t.Fatalf("feeds = %+v", page.Feeds)
	}
	if page.Feeds[0].URL != "https://alice.example/rss.xml" || page.Feeds[0].Type != "rss" {
		t.Errorf("first feed = %+v", page.Feeds[0])
	}
	if page.Micropub != "https://alice.example/micropub" {
		t.Errorf("micropub = %q, want it resolved", page.Micropub)
	}
	if page.Authorization != "https://alice.example/auth" {
		t.Errorf("authorization endpoint = %q", page.Authorization)
	}
}

func TestDiscoverIgnoresUnsafeSchemes(t *testing.T) {
	page, err := Discover(mustURL(t, "https://alice.example/"), []byte(`
<html><body>
  <a rel="me" href="javascript:alert(1)">no</a>
  <a rel="me" href="mailto:alice@example.org">no</a>
  <a rel="me" href="https://real.example/alice">yes</a>
</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.RelMe) != 1 || page.RelMe[0] != "https://real.example/alice" {
		t.Errorf("rel=me = %v; only http and https belong here", page.RelMe)
	}
}

func TestDiscoverHandlesRubbish(t *testing.T) {
	if _, err := Discover(nil, nil); !errors.Is(err, ErrNoDocument) {
		t.Errorf("empty body gave %v", err)
	}
	for _, body := range []string{"<html", "not html at all", "<div class=h-card>"} {
		if _, err := Discover(mustURL(t, "https://a.example/"), []byte(body)); err != nil {
			t.Errorf("Discover(%q) failed: %v", body, err)
		}
	}
}

func TestSameURLIgnoresWWWAndTrailingSlash(t *testing.T) {
	for _, pair := range [][2]string{
		{"https://social.example/users/alice", "https://social.example/users/alice/"},
		{"https://www.social.example/users/alice", "https://social.example/users/alice"},
		{"https://SOCIAL.example/users/alice", "https://social.example/users/alice"},
	} {
		if !SameURL(pair[0], pair[1]) {
			t.Errorf("SameURL(%q, %q) = false", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{
		{"https://social.example/users/alice", "https://social.example/users/bob"},
		{"https://social.example/users/alice", "https://other.example/users/alice"},
	} {
		if SameURL(pair[0], pair[1]) {
			t.Errorf("SameURL(%q, %q) = true", pair[0], pair[1])
		}
	}
}

func testStore(t *testing.T) (*Store, *identity.Account) {
	sites, account, _ := testStoreWithStranger(t)
	return sites, account
}

func testStoreWithStranger(t *testing.T) (*Store, *identity.Account, *identity.Account) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-indieweb")
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

	accounts := identity.NewStore(db)
	account, err := accounts.Create(ctx, "alice@social.example", "a long enough password", identity.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := accounts.Create(ctx, "mallory@social.example", "a long enough password", identity.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db, Options{Domain: "social.example", AllowPrivateAddrs: true}), account, stranger
}

func TestClaimNormalisesAndIsExclusive(t *testing.T) {
	sites, account, stranger := testStoreWithStranger(t)
	ctx := context.Background()

	site, err := sites.Claim(ctx, account.ID, "  WWW.Alice.Example  ")
	if err != nil {
		t.Fatal(err)
	}
	if site.Host != "alice.example" {
		t.Errorf("host = %q, want www stripped and lowercased", site.Host)
	}
	if site.URL != "https://www.alice.example/" {
		t.Errorf("url = %q", site.URL)
	}
	if site.State() != Claimed {
		t.Errorf("a fresh claim is %q, want claimed", site.State())
	}
	if site.Verified() {
		t.Error("a claim counts as verified before anything was checked")
	}

	again, err := sites.Claim(ctx, account.ID, "https://alice.example/")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != site.ID {
		t.Error("claiming the same domain twice made two rows")
	}

	if _, err := sites.Claim(ctx, stranger.ID, "alice.example"); !errors.Is(err, ErrSiteClaimed) {
		t.Errorf("a stranger claimed a taken domain: %v", err)
	}
}

func TestClaimRejectsRubbish(t *testing.T) {
	sites, account := testStore(t)
	for _, bad := range []string{"", "   ", "ftp://alice.example", "javascript:alert(1)", "https://"} {
		if _, err := sites.Claim(context.Background(), account.ID, bad); !errors.Is(err, ErrBadURL) {
			t.Errorf("Claim(%q) gave %v, want ErrBadURL", bad, err)
		}
	}
}

func TestVerificationNeedsTheSiteToLinkBack(t *testing.T) {
	sites, account := testStore(t)
	ctx := context.Background()

	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(body))
	}))
	defer server.Close()

	site, err := sites.Claim(ctx, account.ID, server.URL)
	if err != nil {
		t.Fatal(err)
	}

	body = `<html><head><link rel="me" href="https://somewhere.else/alice"></head><body>hi</body></html>`
	if err := sites.Verify(ctx, site, "alice"); !errors.Is(err, ErrNoBackLink) {
		t.Fatalf("a site that links elsewhere verified: %v", err)
	}

	after, _ := sites.ByID(ctx, site.ID)
	if after.Verified() {
		t.Error("the site is marked verified after a failed check")
	}
	if after.State() != Failing {
		t.Errorf("state = %q, want failing", after.State())
	}
	if after.LastError == "" {
		t.Error("the failure was not recorded")
	}

	body = `<html><head>
		<link rel="me" href="https://social.example/users/alice">
		<link rel="alternate" type="application/rss+xml" href="/rss.xml">
	</head><body>
		<div class="h-card"><span class="p-name">Alice</span><p class="p-note">Feeds.</p></div>
	</body></html>`
	if err := sites.Verify(ctx, site, "alice"); err != nil {
		t.Fatalf("a site that links back did not verify: %v", err)
	}

	verified, _ := sites.ByID(ctx, site.ID)
	if !verified.Verified() || verified.State() != Verified {
		t.Fatalf("site = %+v", verified)
	}
	if verified.LastError != "" {
		t.Errorf("the old failure was not cleared: %q", verified.LastError)
	}
	if verified.Name != "Alice" || verified.Note != "Feeds." {
		t.Errorf("the h-card was not picked up: %+v", verified)
	}
	if verified.FeedURL == "" {
		t.Error("the advertised feed was not discovered")
	}
}

func TestVerificationSurvivesADeadSite(t *testing.T) {
	sites, account := testStore(t)
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer server.Close()

	site, err := sites.Claim(ctx, account.ID, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := sites.Verify(ctx, site, "alice"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a 410 gave %v, want ErrUnreachable", err)
	}

	after, _ := sites.ByID(ctx, site.ID)
	if after.State() != Failing || after.LastError == "" {
		t.Errorf("site = %+v", after)
	}
}

func TestReleasingASiteIsOnlyForItsOwner(t *testing.T) {
	sites, account, stranger := testStoreWithStranger(t)
	ctx := context.Background()

	site, err := sites.Claim(ctx, account.ID, "alice.example")
	if err != nil {
		t.Fatal(err)
	}

	if err := sites.Release(ctx, stranger.ID, site.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("a stranger released someone else's site: %v", err)
	}
	if err := sites.Release(ctx, account.ID, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := sites.ByID(ctx, site.ID); !errors.Is(err, ErrNoSite) {
		t.Error("the site survived being released")
	}

	if _, err := sites.Claim(ctx, stranger.ID, "alice.example"); err != nil {
		t.Errorf("the domain stayed locked after release: %v", err)
	}
}

func TestVerifiedForPicksOnlyAConfirmedSite(t *testing.T) {
	sites, account := testStore(t)
	ctx := context.Background()

	if _, err := sites.Claim(ctx, account.ID, "unverified.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := sites.VerifiedFor(ctx, account.ID); !errors.Is(err, ErrNoSite) {
		t.Errorf("an unverified claim was returned as the verified site: %v", err)
	}
}
