package interop

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/poller"
	"github.com/runawaydevil/rss-expert/internal/publish"
	"github.com/runawaydevil/rss-expert/internal/store"
	"github.com/runawaydevil/rss-expert/internal/web"
)

type instance struct {
	app      *web.App
	t        *testing.T
	db       *store.DB
	domain   string
	server   *httptest.Server
	accounts *identity.Store
	posts    *publish.Store
	sources  *ingest.Store
	poller   *poller.Poller
	owner    *identity.Account
	handle   string
}

func newInstance(t *testing.T, domain string) *instance {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-fed")
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

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	accounts := identity.NewStore(db)

	owner, err := accounts.Create(ctx, "owner@"+domain, "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	posts := publish.NewStore(db, "https://"+domain)
	handle, err := posts.EnsureHandle(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}

	sources := ingest.NewStore(db)

	server := httptest.NewUnstartedServer(nil)
	base := "http://" + server.Listener.Addr().String()
	app := web.New(db, quiet, base, web.Options{ShowPreview: true, ReachPrivate: true, ActivityPub: true})
	server.Config.Handler = app.Handler()
	server.Start()
	t.Cleanup(server.Close)

	posts = publish.NewStore(db, base)

	return &instance{
		t: t, domain: domain, server: server, db: db, app: app,
		accounts: accounts, posts: posts, sources: sources,
		poller: poller.New(sources, quiet, poller.Options{AllowPrivateAddrs: true}),
		owner:  owner, handle: handle,
	}
}

func (i *instance) feedURL() string {
	return i.server.URL + "/users/" + i.handle + "/rss.xml"
}

func (i *instance) publish(title, markdown, inReplyTo string) *publish.Post {
	i.t.Helper()
	post, err := i.posts.Create(context.Background(), i.owner, title, markdown, inReplyTo)
	if err != nil {
		i.t.Fatalf("%s could not publish: %v", i.domain, err)
	}
	return post
}

func (i *instance) follow(feedURL string) *ingest.Source {
	i.t.Helper()
	source, err := i.sources.AddSource(context.Background(), feedURL)
	if err != nil {
		i.t.Fatalf("%s could not follow %s: %v", i.domain, feedURL, err)
	}
	return source
}

func (i *instance) read(source *ingest.Source) {
	i.t.Helper()
	if err := i.poller.Fetch(context.Background(), source); err != nil {
		i.t.Fatalf("%s could not read %s: %v", i.domain, source.FeedURL, err)
	}
	refreshed, err := i.sources.SourceByID(context.Background(), source.ID)
	if err != nil {
		i.t.Fatal(err)
	}
	if refreshed.LastError != "" {
		i.t.Fatalf("%s failed reading %s: %s", i.domain, source.FeedURL, refreshed.LastError)
	}
}

func (i *instance) item(key string) ingest.Item {
	i.t.Helper()
	item, err := i.sources.Item(context.Background(), key)
	if err != nil {
		i.t.Fatalf("%s has no item %s: %v", i.domain, key, err)
	}
	return item
}

func TestTwoInstancesHoldAConversationOverPlainRSS(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	opening := alice.publish("On feeds",
		"A thread has to start **somewhere**, and it travels as *markdown*.", "")

	aliceFromBob := bob.follow(alice.feedURL())
	bob.read(aliceFromBob)

	seen := bob.item(opening.GUID)
	if seen.Markdown != "A thread has to start **somewhere**, and it travels as *markdown*." {
		t.Fatalf("bob did not receive the markdown: %q", seen.Markdown)
	}
	if seen.Title != "On feeds" {
		t.Errorf("bob sees the title as %q", seen.Title)
	}
	if !strings.Contains(seen.HTML, "<strong>somewhere</strong>") {
		t.Errorf("bob sees the html as %q", seen.HTML)
	}

	answer := bob.publish("", "Only if someone answers. And I did.", opening.GUID)
	if answer.InReplyTo != opening.GUID {
		t.Fatalf("bob's reply points at %q, want %q", answer.InReplyTo, opening.GUID)
	}

	bobFromAlice := alice.follow(bob.feedURL())
	alice.read(bobFromAlice)

	returned := alice.item(answer.GUID)
	if returned.InReplyTo != opening.GUID {
		t.Fatalf("the reply came back to alice pointing at %q, want %q", returned.InReplyTo, opening.GUID)
	}

	threaded, err := alice.sources.Replies(ctx, opening.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threaded) != 1 {
		t.Fatalf("alice threads %d replies under her own post, want 1", len(threaded))
	}
	if threaded[0].Key != answer.GUID {
		t.Errorf("alice threaded %q, want %q", threaded[0].Key, answer.GUID)
	}
	if threaded[0].Markdown != "Only if someone answers. And I did." {
		t.Errorf("the reply lost its markdown on the way back: %q", threaded[0].Markdown)
	}

	orphans, err := alice.sources.Orphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range orphans {
		if parent == opening.GUID {
			t.Error("alice treats a reply to her own post as an orphan")
		}
	}
}

func TestTheCommentsFeedCarriesTheReplies(t *testing.T) {
	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	opening := alice.publish("A question", "Does the comments feed work?", "")

	aliceFromBob := bob.follow(alice.feedURL())
	bob.read(aliceFromBob)
	bob.publish("", "It does, and this is the proof.", opening.GUID)

	bobFromAlice := alice.follow(bob.feedURL())
	alice.read(bobFromAlice)

	body := get(t, alice.server.URL+"/p/1/replies.xml")
	if !strings.Contains(body, "<title>Replies to A question</title>") {
		t.Errorf("the replies feed is not titled after the post:\n%s", body)
	}

	aliceReply := alice.publish("", "Answering my own question, for the feed.", opening.GUID)

	body = get(t, alice.server.URL+"/p/1/replies.xml")
	if !strings.Contains(body, aliceReply.GUID) {
		t.Errorf("the replies feed does not carry the reply:\n%s", body)
	}

	own := get(t, alice.feedURL())
	if !strings.Contains(own, "source:comments") {
		t.Errorf("alice's own feed does not advertise a comments feed:\n%s", own)
	}
	if !strings.Contains(own, "/p/1/replies.xml") {
		t.Errorf("the comments element does not point at the replies feed:\n%s", own)
	}
}

func TestAnEditTravelsWithoutChangingTheGUID(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	post := alice.publish("Draft", "First attempt, with a typpo.", "")

	aliceFromBob := bob.follow(alice.feedURL())
	bob.read(aliceFromBob)

	if got := bob.item(post.GUID); !strings.Contains(got.Markdown, "typpo") {
		t.Fatalf("bob did not get the first version: %q", got.Markdown)
	}

	if _, err := alice.posts.Update(ctx, alice.owner, post.ID, "Draft", "Second attempt, corrected."); err != nil {
		t.Fatal(err)
	}
	bob.read(aliceFromBob)

	corrected := bob.item(post.GUID)
	if !strings.Contains(corrected.Markdown, "corrected") {
		t.Errorf("the edit did not reach bob: %q", corrected.Markdown)
	}
	if corrected.Key != post.GUID {
		t.Errorf("the edit arrived under a different guid: %q", corrected.Key)
	}
	if !corrected.Edited() {
		t.Error("bob cannot tell the post was edited")
	}

	timeline, err := bob.sources.Timeline(ctx, 10, time_Zero())
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) != 1 {
		t.Errorf("an edit created %d timeline entries, want 1", len(timeline))
	}
}

func TestReadingTheSameFeedTwiceAddsNothing(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	alice.publish("One", "The only post.", "")

	source := bob.follow(alice.feedURL())
	bob.read(source)
	_, firstObservations, firstItems, err := bob.sources.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	bob.read(source)
	_, secondObservations, secondItems, err := bob.sources.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if secondObservations != firstObservations || secondItems != firstItems {
		t.Errorf("a second read grew the database from %d/%d to %d/%d",
			firstObservations, firstItems, secondObservations, secondItems)
	}
}

func time_Zero() time.Time { return time.Time{} }

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
