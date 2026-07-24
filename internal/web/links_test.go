package web

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

var anchorHref = regexp.MustCompile(`<a\b[^>]*\bhref="(/[^"{}]*)"`)

func templateLinks(t *testing.T) []string {
	t.Helper()

	seen := map[string]bool{}
	err := fs.WalkDir(assetFiles, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".html") {
			return err
		}
		body, err := fs.ReadFile(assetFiles, name)
		if err != nil {
			return err
		}
		for _, m := range anchorHref.FindAllStringSubmatch(string(body), -1) {
			seen[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	out := make([]string, 0, len(seen))
	for link := range seen {
		out = append(out, link)
	}
	sort.Strings(out)
	return out
}

func TestEveryLinkInEveryTemplateResolves(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	owner := signedInAs(t, h, accounts, "owner@example.org", identity.RoleOwner)

	links := templateLinks(t)
	if len(links) < 10 {
		t.Fatalf("only %d links found; the scan is not seeing the templates", len(links))
	}

	for _, link := range links {
		resp := getAs(t, h, link, owner)
		switch resp.StatusCode {
		case http.StatusNotFound:
			t.Errorf("%s is linked from a template but answers 404", link)
		case http.StatusMethodNotAllowed:
			t.Errorf("%s is linked as a normal link but refuses GET", link)
		}
	}
}

func TestOurOwnFeedIsNotASubscription(t *testing.T) {
	ctx := context.Background()

	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	app := New(db, quietLogger(), "https://rss.expert", Options{})
	h := app.Handler()

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "owner@rss.expert", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	posts := publish.NewStore(db, "https://rss.expert")
	if _, err := posts.EnsureHandle(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Create(ctx, owner, "Mine", "A post of my own.", ""); err != nil {
		t.Fatal(err)
	}

	sources := ingest.NewStore(db)

	var total int
	db.Read.QueryRowContext(ctx, `select count(*) from source`).Scan(&total)
	if total == 0 {
		t.Fatal("publishing did not create the local projection at all")
	}

	listed, err := sources.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range listed {
		if strings.Contains(s.FeedURL, "rss.expert") {
			t.Errorf("this instance's own feed is listed as a subscription: %s", s.FeedURL)
		}
	}

	page, _ := io.ReadAll(get(t, h, "/sources").Body)
	if strings.Contains(string(page), "rss.expert/users/") {
		t.Errorf("the sources page shows our own feed:\n%s", page)
	}
}

func TestTheRepliesLinkDoesNotHardcodeTheDomain(t *testing.T) {
	ctx := context.Background()

	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	app := New(db, quietLogger(), "https://old-domain.example", Options{})
	h := app.Handler()

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "owner@example.org", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	posts := publish.NewStore(db, "https://old-domain.example")
	if _, err := posts.EnsureHandle(ctx, owner); err != nil {
		t.Fatal(err)
	}
	post, err := posts.Create(ctx, owner, "Anything", "Body.", "")
	if err != nil {
		t.Fatal(err)
	}

	page, _ := io.ReadAll(get(t, h, "/p/"+strconv.FormatInt(post.ID, 10)).Body)
	if strings.Contains(string(page), `href="https://old-domain.example`) {
		t.Errorf("the post page links out to the configured domain; a domain change breaks it:\n%s", page)
	}
	if !strings.Contains(string(page), `href="/p/`) {
		t.Error("the replies link is not a relative path")
	}
}
