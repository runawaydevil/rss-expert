package web

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

func TestIndexableAllowsPublicPagesOnly(t *testing.T) {
	index := map[string]bool{
		"/":            true,
		"/rules":       true,
		"/users/pablo": true,
		"/p/7":         true,
		"/p/7/edit":    false,
		"/settings":    false,
		"/admin":       false,
		"/write":       false,
		"/login":       false,
	}
	for path, want := range index {
		if got := indexable(path); got != want {
			t.Errorf("indexable(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestTextExcerptStripsAndTruncates(t *testing.T) {
	got := textExcerpt("<p>Hello <strong>there</strong>, reader.</p>", 160)
	if got != "Hello there, reader." {
		t.Errorf("excerpt = %q", got)
	}
	long := textExcerpt("<p>"+strings.Repeat("word ", 100)+"</p>", 20)
	if !strings.HasSuffix(long, "…") {
		t.Errorf("a long excerpt should be truncated with an ellipsis, got %q", long)
	}
	if len([]rune(strings.TrimSuffix(long, "…"))) > 20 {
		t.Errorf("excerpt kept too many runes: %q", long)
	}
}

func TestHomeCarriesSEOMetadata(t *testing.T) {
	db := tempDB(t)
	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := New(db, quietLogger(), "https://rss.expert", Options{}).Handler()

	body, _ := io.ReadAll(get(t, h, "/").Body)
	page := string(body)

	for _, want := range []string{
		`<meta name="description"`,
		`<link rel="canonical" href="https://rss.expert/">`,
		`<meta property="og:title"`,
		`<meta name="twitter:card"`,
		`content="index, follow"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the home page is missing %q", want)
		}
	}
}

func TestSettingsIsNoIndex(t *testing.T) {
	ctx := context.Background()
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(ctx, "owner@test.example", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}
	session := signIn(t, h, "owner@test.example")

	body, _ := io.ReadAll(getAs(t, h, "/settings", session).Body)
	if !strings.Contains(string(body), "noindex") {
		t.Errorf("the settings page should be noindex:\n%s", body)
	}
}

func TestPostPageAnnouncesAnArticle(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := New(db, quietLogger(), "https://rss.expert", Options{}).Handler()

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "owner@rss.expert", testPassword, identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	posts := publish.NewStore(db, "https://rss.expert")
	if _, err := posts.EnsureHandle(ctx, owner); err != nil {
		t.Fatal(err)
	}
	post, err := posts.Create(ctx, owner, "A title", "Some words that make a body.", "")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(get(t, h, "/p/"+strconv.FormatInt(post.ID, 10)).Body)
	page := string(body)
	for _, want := range []string{`content="article"`, `article:published_time`, `class="post h-entry"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the post page is missing %q", want)
		}
	}
}

func TestRobotsAndSitemapAreServed(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := New(db, quietLogger(), "https://rss.expert", Options{}).Handler()

	robots := get(t, h, "/robots.txt")
	if ct := robots.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("robots.txt content type = %q", ct)
	}
	rbody, _ := io.ReadAll(robots.Body)
	for _, want := range []string{"Disallow: /settings", "Sitemap: https://rss.expert/sitemap.xml"} {
		if !strings.Contains(string(rbody), want) {
			t.Errorf("robots.txt is missing %q:\n%s", want, rbody)
		}
	}

	sitemap := get(t, h, "/sitemap.xml")
	if sitemap.StatusCode != http.StatusOK {
		t.Fatalf("sitemap status = %d", sitemap.StatusCode)
	}
	sbody, _ := io.ReadAll(sitemap.Body)
	for _, want := range []string{"<urlset", "https://rss.expert/", "https://rss.expert/rules"} {
		if !strings.Contains(string(sbody), want) {
			t.Errorf("sitemap is missing %q:\n%s", want, sbody)
		}
	}
}
