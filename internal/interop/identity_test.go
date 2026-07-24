package interop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/indieweb"
)

func TestADomainAndAProfileVerifyEachOther(t *testing.T) {
	ctx := context.Background()
	alice := newInstance(t, "alice.test")
	alice.publish("Hello", "A first post so the profile is not empty.", "")

	sites := indieweb.NewStore(alice.db, indieweb.Options{
		Domain:            alice.server.URL,
		AllowPrivateAddrs: true,
	})

	var linkBack string
	personal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head>%s
			<link rel="alternate" type="application/rss+xml" href="/feed.xml">
		</head><body>
			<div class="h-card">
				<span class="p-name">Alice of the Open Web</span>
				<p class="p-note">Writes in public.</p>
			</div>
		</body></html>`, linkBack)
	}))
	defer personal.Close()

	site, err := sites.Claim(ctx, alice.owner.ID, personal.URL)
	if err != nil {
		t.Fatal(err)
	}
	if site.Verified() {
		t.Fatal("a claim starts out verified")
	}

	if err := sites.Verify(ctx, site, alice.handle); err == nil {
		t.Fatal("a site with no link back verified")
	}

	linkBack = fmt.Sprintf(`<link rel="me" href="%s">`, sites.ProfileURL(alice.handle))
	if err := sites.Verify(ctx, site, alice.handle); err != nil {
		t.Fatalf("verification failed once the link was there: %v", err)
	}

	verified, err := sites.ByID(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Verified() {
		t.Fatal("the site is still not verified")
	}
	if verified.Name != "Alice of the Open Web" {
		t.Errorf("the h-card name was not read: %q", verified.Name)
	}
	if verified.FeedURL != personal.URL+"/feed.xml" {
		t.Errorf("the advertised feed was not found: %q", verified.FeedURL)
	}

	profile := get(t, alice.server.URL+"/users/"+alice.handle)
	if !strings.Contains(profile, `rel="me"`) {
		t.Error("the profile page carries no rel=me; verification only works if both ends link")
	}
	if !strings.Contains(profile, "verified") {
		t.Error("the profile does not say the domain is verified")
	}
	if !strings.Contains(profile, "Alice of the Open Web") {
		t.Error("the profile does not show the name from the h-card")
	}
}

func TestAnUnverifiedClaimIsShownAsAClaim(t *testing.T) {
	ctx := context.Background()
	alice := newInstance(t, "alice.test")
	alice.publish("Hello", "A post.", "")

	sites := indieweb.NewStore(alice.db, indieweb.Options{
		Domain:            alice.server.URL,
		AllowPrivateAddrs: true,
	})
	if _, err := sites.Claim(ctx, alice.owner.ID, "https://not-really-mine.example/"); err != nil {
		t.Fatal(err)
	}

	profile := get(t, alice.server.URL+"/users/"+alice.handle)
	if strings.Contains(profile, "identity-verified") {
		t.Error("an unverified claim is displayed with the verified badge")
	}
	if !strings.Contains(profile, "not-really-mine.example") {
		t.Error("the claim is not shown at all; it should be, marked as unverified")
	}
	if !strings.Contains(profile, "nobody has checked this yet") {
		t.Error("the page does not say the claim is unchecked")
	}
}
