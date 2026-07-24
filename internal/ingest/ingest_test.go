package ingest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/feedin"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-ingest")
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
	return NewStore(db)
}

func corpus(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func ingestCorpus(t *testing.T, s *Store, feedURL, name string) (*Source, Result) {
	t.Helper()
	ctx := context.Background()

	source, err := s.AddSource(ctx, feedURL)
	if err != nil {
		t.Fatal(err)
	}
	raw := corpus(t, name)
	parsed, err := feedin.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Ingest(ctx, source, raw, "application/rss+xml", parsed)
	if err != nil {
		t.Fatal(err)
	}
	return source, result
}

func TestIngestRealFirehose(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	source, result := ingestCorpus(t, s, "https://rss.chat/users/rss.xml", "rsschat-firehose.xml")

	if !result.PayloadStored {
		t.Error("the payload was not stored")
	}
	if result.Observations != 100 {
		t.Errorf("recorded %d observations, want 100", result.Observations)
	}
	if result.Converged != 100 {
		t.Errorf("converged %d items, want 100", result.Converged)
	}

	payloads, observations, items, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if payloads != 1 || observations != 100 || items != 100 {
		t.Errorf("counts: payloads=%d observations=%d items=%d", payloads, observations, items)
	}

	refreshed, err := s.SourceByID(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Title != "rss.chat: all posts" {
		t.Errorf("source title = %q", refreshed.Title)
	}
	if refreshed.SelfURL != "https://rss.chat/users/rss.xml" {
		t.Errorf("source self = %q", refreshed.SelfURL)
	}
}

func TestIngestingTheSameFeedTwiceChangesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	source, first := ingestCorpus(t, s, "https://rss.chat/users/rss.xml", "rsschat-firehose.xml")

	raw := corpus(t, "rsschat-firehose.xml")
	parsed, err := feedin.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Ingest(ctx, source, raw, "application/rss+xml", parsed)
	if err != nil {
		t.Fatal(err)
	}

	if second.PayloadStored {
		t.Error("the identical payload was stored a second time")
	}
	if second.Observations != 0 {
		t.Errorf("re-reading the same feed recorded %d new observations", second.Observations)
	}
	if second.Converged != 0 {
		t.Errorf("re-reading the same feed changed %d winners", second.Converged)
	}

	_, observations, items, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observations != first.Observations || items != first.Observations {
		t.Errorf("after a repeat read: observations=%d items=%d, want %d of each",
			observations, items, first.Observations)
	}
}

func TestPayloadSurvivesRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ingestCorpus(t, s, "https://scripting.com/rss.xml", "scripting-com.xml")

	var sum []byte
	if err := s.db.Read.QueryRowContext(ctx, `select sha256 from raw_payload`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	back, err := s.Payload(ctx, sum)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, corpus(t, "scripting-com.xml")) {
		t.Error("the stored payload does not decompress to the original bytes")
	}

	var stored, length int
	if err := s.db.Read.QueryRowContext(ctx,
		`select length(body), byte_length from raw_payload`).Scan(&stored, &length); err != nil {
		t.Fatal(err)
	}
	if stored >= length {
		t.Errorf("compression made it bigger: %d stored for %d original", stored, length)
	}
	t.Logf("compressed %d bytes to %d (%.1f×)", length, stored, float64(length)/float64(stored))
}

func TestThreadingIsRecorded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ingestCorpus(t, s, "https://rss.chat/users/rss.xml", "rsschat-firehose.xml")

	var replies int
	if err := s.db.Read.QueryRowContext(ctx,
		`select count(*) from logical_item where in_reply_to is not null`).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if replies != 65 {
		t.Errorf("%d items are marked as replies, want the 65 the corpus carries", replies)
	}

	orphans, err := s.Orphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d replies point at a parent this instance has not seen", len(orphans))

	var parent string
	if err := s.db.Read.QueryRowContext(ctx,
		`select in_reply_to from logical_item
		 where in_reply_to in (select item_key from logical_item) limit 1`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	children, err := s.Replies(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) == 0 {
		t.Errorf("no replies found for %s even though one points at it", parent)
	}
}

func TestTimelineIsNewestFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ingestCorpus(t, s, "https://rss.chat/users/rss.xml", "rsschat-firehose.xml")

	items, err := s.Timeline(ctx, 20, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 20 {
		t.Fatalf("timeline returned %d items", len(items))
	}

	for i := 1; i < len(items); i++ {
		if items[i].Published.After(items[i-1].Published) {
			t.Fatalf("item %d is newer than the one before it", i)
		}
	}

	first := items[0]
	if first.DisplayAuthor() == "" {
		t.Error("the newest item has nobody to attribute it to")
	}
	if first.Host() == "" {
		t.Error("the newest item has no host to show")
	}
	if first.Reason == "" {
		t.Error("the newest item records no reason for its winning version")
	}
}

func TestTheAuthorsOwnCopyWins(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	firehose, err := s.AddSource(ctx, "https://aggregator.example/all.xml")
	if err != nil {
		t.Fatal(err)
	}
	personal, err := s.AddSource(ctx, "https://alice.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}

	published := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	thin := &feed.Feed{Items: []feed.Item{{
		GUID:      "https://alice.example/p/1",
		Link:      "https://alice.example/p/1",
		HTML:      "<p>relayed copy</p>",
		Published: published,
	}}}
	rich := &feed.Feed{Items: []feed.Item{{
		GUID:      "https://alice.example/p/1",
		Link:      "https://alice.example/p/1",
		HTML:      "<p>the author's own copy</p>",
		Markdown:  "the author's own copy",
		Published: published,
	}}}

	if _, err := s.Ingest(ctx, firehose, []byte("<rss>aggregated</rss>"), "application/rss+xml", thin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, personal, []byte("<rss>personal</rss>"), "application/rss+xml", rich); err != nil {
		t.Fatal(err)
	}

	item, err := s.Item(ctx, "https://alice.example/p/1")
	if err != nil {
		t.Fatal(err)
	}
	if item.Markdown != "the author's own copy" {
		t.Errorf("the relayed copy won: %+v", item)
	}
	if item.SourceID != personal.ID {
		t.Errorf("winning observation came from source %d, want the author's own feed %d",
			item.SourceID, personal.ID)
	}
	if item.Reason != "came from the author's own domain" {
		t.Errorf("reason = %q", item.Reason)
	}
}

func TestASlowPollDoesNotUndoAnEdit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	source, err := s.AddSource(ctx, "https://alice.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}

	published := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	edited := &feed.Feed{Items: []feed.Item{{
		GUID: "https://alice.example/p/1", Link: "https://alice.example/p/1",
		HTML: "<p>corrected</p>", Published: published, Updated: published.Add(90 * time.Minute),
	}}}
	original := &feed.Feed{Items: []feed.Item{{
		GUID: "https://alice.example/p/1", Link: "https://alice.example/p/1",
		HTML: "<p>first draft</p>", Published: published,
	}}}

	if _, err := s.Ingest(ctx, source, []byte("<rss>push</rss>"), "application/rss+xml", edited); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, source, []byte("<rss>slow poll</rss>"), "application/rss+xml", original); err != nil {
		t.Fatal(err)
	}

	item, err := s.Item(ctx, "https://alice.example/p/1")
	if err != nil {
		t.Fatal(err)
	}
	if item.HTML != "<p>corrected</p>" {
		t.Errorf("a late poll of the old version overwrote the edit: %q", item.HTML)
	}
}

func TestPollIntervalAdaptsAndBacksOff(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	source, err := s.AddSource(ctx, "https://quiet.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RecordFetch(ctx, source.ID, FetchOutcome{Status: 200, Cadence: 48 * time.Hour}, now); err != nil {
		t.Fatal(err)
	}
	after, _ := s.SourceByID(ctx, source.ID)
	if after.PollInterval != MaxPollInterval {
		t.Errorf("a source publishing every two days is polled every %v, want the daily ceiling", after.PollInterval)
	}

	busy, err := s.AddSource(ctx, "https://busy.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFetch(ctx, busy.ID, FetchOutcome{Status: 200, Cadence: time.Minute}, now); err != nil {
		t.Fatal(err)
	}
	after, _ = s.SourceByID(ctx, busy.ID)
	if after.PollInterval != MinPollInterval {
		t.Errorf("a very busy source is polled every %v, want the floor of %v", after.PollInterval, MinPollInterval)
	}

	for i := 0; i < MaxFailuresBeforeBackoff; i++ {
		if err := s.RecordFetch(ctx, busy.ID, FetchOutcome{Status: 503}, now); err != nil {
			t.Fatal(err)
		}
	}
	after, _ = s.SourceByID(ctx, busy.ID)
	if after.FailureCount < MaxFailuresBeforeBackoff {
		t.Errorf("failures = %d", after.FailureCount)
	}
	if after.LastError == "" {
		t.Error("a failing source records no reason")
	}
	if after.PollInterval <= MinPollInterval {
		t.Errorf("a failing source is still polled every %v; it should back off", after.PollInterval)
	}
}

func TestDueSkipsQuarantinedSources(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Hour)

	good, err := s.AddSource(ctx, "https://good.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := s.AddSource(ctx, "https://bad.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Quarantine(ctx, bad.ID, now); err != nil {
		t.Fatal(err)
	}

	due, err := s.Due(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != good.ID {
		t.Errorf("due = %+v, want only the source that is not quarantined", due)
	}
}

func TestAddingTheSameFeedTwiceReturnsTheSameSource(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first, err := s.AddSource(ctx, "https://Example.ORG/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddSource(ctx, "https://example.org/rss.xml#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("the same feed in different spellings made two sources: %d and %d", first.ID, second.ID)
	}
}

func TestUnusableFeedURLsAreRefused(t *testing.T) {
	s := testStore(t)
	for _, bad := range []string{"", "not a url", "ftp://example.org/f.xml", "file:///etc/passwd", "https://"} {
		if _, err := s.AddSource(context.Background(), bad); err == nil {
			t.Errorf("AddSource(%q) was accepted", bad)
		}
	}
}

func TestStoredPayloadsAreDroppedOnceNothingRefersToThem(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	source, _ := ingestCorpus(t, s, "https://rss.chat/users/rss.xml", "rsschat-firehose.xml")

	payloads, _, _, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if payloads != 1 {
		t.Fatalf("payloads = %d before any purge", payloads)
	}

	later := time.Now().Add(CleanRetention + time.Hour)
	if n, err := s.PurgeExpiredPayloads(ctx, later); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("purged %d payloads that observations still point at", n)
	}

	if _, err := s.db.Write.ExecContext(ctx, `delete from logical_item`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Write.ExecContext(ctx, `delete from observation where source_id = ?`, source.ID); err != nil {
		t.Fatal(err)
	}

	if n, err := s.PurgeExpiredPayloads(ctx, later); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Errorf("purged %d payloads, want 1", n)
	}

	if n, err := s.PurgeExpiredPayloads(ctx, time.Now()); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("a second sweep removed %d more rows", n)
	}
}

func TestRemovingASourceKeepsWhatOthersAlsoSaw(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	shared := `<?xml version="1.0"?>
	<rss version="2.0"><channel><title>%s</title><link>https://a.example/</link>
	<description>d</description>
	<item><title>Shared post</title><link>https://origin.example/1</link>
	<guid isPermaLink="true">https://origin.example/1</guid>
	<pubDate>Mon, 20 Jul 2026 10:00:00 GMT</pubDate></item>
	</channel></rss>`

	for _, name := range []string{"first", "second"} {
		source, err := s.AddSource(ctx, "https://"+name+".example/rss.xml")
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(fmt.Sprintf(shared, name))
		parsed, err := feedin.Parse(body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Ingest(ctx, source, body, "application/rss+xml", parsed); err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.SourceByURL(ctx, "https://first.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSource(ctx, first.ID); err != nil {
		t.Fatalf("removing a source failed: %v", err)
	}

	if _, err := s.Item(ctx, "https://origin.example/1"); err != nil {
		t.Errorf("the item vanished even though another source still carries it: %v", err)
	}

	list, err := s.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("%d sources remain, want 1", len(list))
	}
}

func TestRemovingTheOnlySourceTakesItsItems(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	source, _ := ingestCorpus(t, s, "https://rss.chat/users/rss.xml", "rsschat-firehose.xml")

	_, _, before, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("nothing was ingested")
	}

	if err := s.RemoveSource(ctx, source.ID); err != nil {
		t.Fatalf("removing the source failed: %v", err)
	}

	_, observations, items, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observations != 0 || items != 0 {
		t.Errorf("after removing the only source: %d observations, %d items", observations, items)
	}
}
