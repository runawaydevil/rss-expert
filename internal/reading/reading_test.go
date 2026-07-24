package reading

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/feedin"
	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T) (*Store, *ingest.Store, *identity.Account) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-reading")
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

	account, err := identity.NewStore(db).Create(ctx, "reader@example.org", "a long enough password", identity.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	return New(db), ingest.NewStore(db), account
}

func seedFirehose(t *testing.T, sources *ingest.Store) *ingest.Source {
	t.Helper()
	ctx := context.Background()

	source, err := sources.AddSource(ctx, "https://rss.chat/users/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", "rsschat-firehose.xml"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := feedin.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sources.Ingest(ctx, source, raw, "application/rss+xml", parsed); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestUnreadShrinksAsYouRead(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	before, err := reading.UnreadCount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before != 100 {
		t.Fatalf("unread = %d, want every item in the corpus", before)
	}

	items, err := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	if err := reading.MarkRead(ctx, account.ID, keys...); err != nil {
		t.Fatal(err)
	}

	after, err := reading.UnreadCount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before-5 {
		t.Errorf("unread = %d after reading 5 of %d", after, before)
	}

	unread, err := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 10, UnreadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range unread {
		for _, read := range keys {
			if item.Key == read {
				t.Errorf("%s is still in the unread list", item.Key)
			}
		}
	}
}

func TestMarkingUnreadPutsItBack(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	items, _ := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 1})
	key := items[0].Key

	if err := reading.MarkRead(ctx, account.ID, key); err != nil {
		t.Fatal(err)
	}
	if err := reading.MarkUnread(ctx, account.ID, key); err != nil {
		t.Fatal(err)
	}

	flags, err := reading.FlagsFor(ctx, account.ID, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	if flags[key].Read {
		t.Error("the item is still marked read")
	}
}

func TestSavedListIsNewestSavedFirst(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	items, _ := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 3})
	for _, item := range items {
		if err := reading.Save(ctx, account.ID, item.Key); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}

	count, err := reading.SavedCount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("saved = %d, want 3", count)
	}

	saved, err := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 10, SavedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 {
		t.Fatalf("the saved list holds %d", len(saved))
	}

	if err := reading.Unsave(ctx, account.ID, items[0].Key); err != nil {
		t.Fatal(err)
	}
	if count, _ := reading.SavedCount(ctx, account.ID); count != 2 {
		t.Errorf("saved = %d after unsaving one", count)
	}
}

func TestMarkAllReadClearsTheBacklog(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	n, err := reading.MarkAllRead(ctx, account.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if n != 100 {
		t.Errorf("marked %d read, want 100", n)
	}
	if unread, _ := reading.UnreadCount(ctx, account.ID); unread != 0 {
		t.Errorf("%d items are still unread", unread)
	}
}

func TestSearchFindsWhatWasIngested(t *testing.T) {
	reading, sources, _ := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	hits, err := reading.Search(ctx, "security", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("searching the corpus for a word it contains found nothing")
	}
	if hits[0].Key == "" || hits[0].Snippet == "" {
		t.Errorf("hit = %+v", hits[0])
	}

	none, err := reading.Search(ctx, "zzzzunlikelyzzzz", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("a nonsense query returned %d hits", len(none))
	}
}

func TestSearchSurvivesHostileInput(t *testing.T) {
	reading, sources, _ := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	for _, query := range []string{
		`" OR 1=1 --`, `NEAR(`, `*`, `""`, `AND OR NOT`,
		`security OR (`, `"unclosed`, strings.Repeat("a ", 200),
	} {
		if _, err := reading.Search(ctx, query, 10); err != nil {
			t.Errorf("Search(%q) failed: %v", query, err)
		}
	}
}

func TestSearchIsUpdatedWhenAnItemConverges(t *testing.T) {
	reading, sources, _ := testStore(t)
	ctx := context.Background()

	source, err := sources.AddSource(ctx, "https://alice.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}

	first := &feed.Feed{Items: []feed.Item{{
		GUID: "https://alice.example/p/1", Link: "https://alice.example/p/1",
		Title: "Original", HTML: "<p>strawberries</p>", Published: time.Now().Add(-time.Hour),
	}}}
	if _, err := sources.Ingest(ctx, source, []byte("<rss>1</rss>"), "application/rss+xml", first); err != nil {
		t.Fatal(err)
	}

	if hits, _ := reading.Search(ctx, "strawberries", 10); len(hits) != 1 {
		t.Fatalf("the first version is not searchable: %d hits", len(hits))
	}

	second := &feed.Feed{Items: []feed.Item{{
		GUID: "https://alice.example/p/1", Link: "https://alice.example/p/1",
		Title: "Corrected", HTML: "<p>raspberries</p>",
		Published: time.Now().Add(-time.Hour), Updated: time.Now(),
	}}}
	if _, err := sources.Ingest(ctx, source, []byte("<rss>2</rss>"), "application/rss+xml", second); err != nil {
		t.Fatal(err)
	}

	if hits, _ := reading.Search(ctx, "raspberries", 10); len(hits) != 1 {
		t.Errorf("the edit is not searchable")
	}
	if hits, _ := reading.Search(ctx, "strawberries", 10); len(hits) != 0 {
		t.Errorf("the superseded version is still in the index (%d hits)", len(hits))
	}
}

func TestCollections(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	source := seedFirehose(t, sources)

	collection, err := reading.CreateCollection(ctx, account.ID, "  People  ")
	if err != nil {
		t.Fatal(err)
	}
	if collection.Name != "People" {
		t.Errorf("name = %q, want it trimmed", collection.Name)
	}

	if _, err := reading.CreateCollection(ctx, account.ID, "People"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a duplicate name was accepted: %v", err)
	}
	if _, err := reading.CreateCollection(ctx, account.ID, "   "); !errors.Is(err, ErrCollectionName) {
		t.Errorf("an empty name was accepted: %v", err)
	}

	if err := reading.AddToCollection(ctx, account.ID, collection.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := reading.AddToCollection(ctx, account.ID, collection.ID, source.ID); err != nil {
		t.Errorf("adding the same source twice failed: %v", err)
	}

	all, err := reading.Collections(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Sources != 1 {
		t.Errorf("collections = %+v", all)
	}

	ids, err := reading.CollectionSources(ctx, account.ID, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 5, SourceIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Error("filtering the timeline by a collection returned nothing")
	}
}

func TestACollectionBelongsToOneAccount(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	source := seedFirehose(t, sources)

	collection, err := reading.CreateCollection(ctx, account.ID, "Mine")
	if err != nil {
		t.Fatal(err)
	}

	const stranger = int64(99)
	if err := reading.AddToCollection(ctx, stranger, collection.ID, source.ID); !errors.Is(err, ErrNoCollection) {
		t.Errorf("a stranger added a source to someone else's collection: %v", err)
	}
	if err := reading.DeleteCollection(ctx, stranger, collection.ID); !errors.Is(err, ErrNoCollection) {
		t.Errorf("a stranger deleted someone else's collection: %v", err)
	}
	if _, err := reading.CollectionSources(ctx, stranger, collection.ID); !errors.Is(err, ErrNoCollection) {
		t.Errorf("a stranger read someone else's collection: %v", err)
	}
}

func TestReadStateIsPerAccount(t *testing.T) {
	reading, sources, account := testStore(t)
	ctx := context.Background()
	seedFirehose(t, sources)

	items, _ := sources.Select(ctx, ingest.Query{AccountID: account.ID, Limit: 1})
	if err := reading.MarkRead(ctx, account.ID, items[0].Key); err != nil {
		t.Fatal(err)
	}

	const other = int64(99)
	flags, err := reading.FlagsFor(ctx, other, []string{items[0].Key})
	if err != nil {
		t.Fatal(err)
	}
	if flags[items[0].Key].Read {
		t.Error("one account's read state leaked into another's")
	}
}
