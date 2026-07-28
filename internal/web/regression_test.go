package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/moderation"
)

func TestTheWebFormRunsThePostPublishPipeline(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	app := New(db, quietLogger(), "https://test.example", Options{})
	h := app.Handler()
	accounts := identity.NewStore(db)

	session := signedInAs(t, h, accounts, "alice@example.org", identity.RoleUser)
	alice, err := accounts.ByEmail(ctx, "alice@example.org")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := app.posts.Create(ctx, alice, "Parent", "The opening post.", "")
	if err != nil {
		t.Fatal(err)
	}

	csrf, cookies := csrfFrom(t, h, "/write?to="+url.QueryEscape(parent.GUID), session)
	resp := postForm(t, h, "/write", url.Values{
		"csrf":        {csrf},
		"markdown":    {"A reply from the browser."},
		"in_reply_to": {parent.GUID},
	}, cookies)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("publishing from the form returned %d", resp.StatusCode)
	}

	var jobs int
	if err := db.Read.QueryRowContext(ctx,
		`select count(*) from job where kind = 'webmention.send'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("the browser publication queued %d webmentions, want 1", jobs)
	}
}

func TestAReplyAnnouncesItsParentsCommentsFeed(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	app := New(db, quietLogger(), "https://test.example", Options{})
	accounts := identity.NewStore(db)
	alice, err := accounts.Create(ctx, "alice@example.org", testPassword, identity.RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	parent, err := app.posts.Create(ctx, alice, "Parent", "Opening.", "")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := app.posts.Create(ctx, alice, "", "Reply.", parent.GUID)
	if err != nil {
		t.Fatal(err)
	}

	topics := app.announcementTopics(ctx, reply)
	if !slices.Contains(topics, app.posts.RepliesURL(parent.ID)) {
		t.Fatalf("reply topics %v do not include the parent's comments feed", topics)
	}
	if slices.Contains(topics, app.posts.RepliesURL(reply.ID)) {
		t.Fatalf("reply topics %v incorrectly include the reply's own comments feed", topics)
	}
}

func TestEveryBlockKindIsAppliedToTheTimeline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  moderation.Kind
		value string
	}{
		{name: "item", kind: moderation.Item, value: "https://blocked.example/post"},
		{name: "source", kind: moderation.Source, value: "https://feeds.example/rss.xml"},
		{name: "domain", kind: moderation.Domain, value: "blocked.example"},
		{name: "word", kind: moderation.Word, value: "forbidden phrase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := tempDB(t)
			if _, err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			app := New(db, quietLogger(), "https://test.example", Options{})
			account, err := identity.NewStore(db).Create(ctx,
				tc.name+"@example.org", testPassword, identity.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.moderation.Block(ctx, account, account.ID, tc.kind, tc.value, "test"); err != nil {
				t.Fatal(err)
			}

			items := []ingest.Item{{
				Key:           "https://blocked.example/post",
				Link:          "https://blocked.example/post",
				SourceFeedURL: "https://feeds.example/rss.xml",
				Title:         "A forbidden phrase appears here",
			}}
			if visible := app.visibleItems(ctx, account.ID, items); len(visible) != 0 {
				t.Fatalf("%s block left %d item(s) visible", tc.kind, len(visible))
			}
		})
	}
}

func TestARemoteActorCannotBeRefreshedOrUnsubscribed(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	app := New(db, quietLogger(), "https://test.example", Options{})
	h := app.Handler()
	accounts := identity.NewStore(db)

	session := signedInAs(t, h, accounts, "alice@example.org", identity.RoleUser)
	actor, err := app.sources.EnsureFederatedSource(ctx, "https://mastodon.example/users/bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/sources/refresh", "/sources/remove"} {
		csrf, cookies := csrfFrom(t, h, "/sources", session)
		resp := postForm(t, h, path, url.Values{
			"csrf":   {csrf},
			"source": {strconv.FormatInt(actor.ID, 10)},
		}, cookies)
		if resp.StatusCode == http.StatusSeeOther {
			t.Errorf("%s acted on a remote actor", path)
		}
	}

	if _, err := app.sources.SourceByID(ctx, actor.ID); err != nil {
		t.Fatalf("the actor was removed through the subscription form: %v", err)
	}
}
