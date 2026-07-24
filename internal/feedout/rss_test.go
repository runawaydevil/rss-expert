package feedout

import (
	"bytes"
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/feedin"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "golden", name)
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := goldenPath(name)

	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/feedout -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not match golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

var buildTime = at("2026-07-23T18:00:00Z")

func emitOptions() RSSOptions {
	return RSSOptions{
		Generator: "rss-expert",
		Docs:      "https://www.rssboard.org/rss-specification",
		BuildTime: buildTime,
	}
}

func conversationRoot() *feed.Feed {
	return &feed.Feed{
		Title:       "Alice",
		Link:        "https://alice.example/",
		Description: "Notes from Alice",
		Language:    "en-us",
		Updated:     at("2026-07-23T17:40:00Z"),
		Self:        "https://alice.example/rss.xml",
		Accounts: []feed.Account{
			{Service: "bluesky", Handle: "@alice.example"},
		},
		Items: []feed.Item{{
			GUID:            "https://alice.example/p/1",
			GUIDIsPermalink: true,
			Title:           "On feeds",
			HTML:            "<p>A thread has to start <em>somewhere</em>.</p>",
			Markdown:        "A thread has to start *somewhere*.",
			Published:       at("2026-07-23T17:40:00Z"),
			Comments: &feed.Comments{
				Count:   2,
				FeedURL: "https://alice.example/p/1/replies.xml",
			},
		}},
	}
}

func conversationReplies() *feed.Feed {
	return &feed.Feed{
		Title:       "Replies to On feeds",
		Link:        "https://alice.example/p/1",
		Description: "Replies to a post by Alice",
		Updated:     at("2026-07-23T17:55:00Z"),
		Self:        "https://alice.example/p/1/replies.xml",
		Items: []feed.Item{
			{
				GUID:            "https://bob.example/p/7",
				GUIDIsPermalink: true,
				HTML:            "<p>Only if someone answers.</p>",
				Markdown:        "Only if someone answers.",
				Published:       at("2026-07-23T17:48:00Z"),
				InReplyTo:       "https://alice.example/p/1",
				Source:          &feed.Source{URL: "https://bob.example/rss.xml", Name: "Bob"},
				Comments: &feed.Comments{
					Count:   1,
					FeedURL: "https://bob.example/p/7/replies.xml",
				},
			},
			{
				GUID:            "https://alice.example/p/2",
				GUIDIsPermalink: true,
				HTML:            "<p>Someone did.</p>",
				Markdown:        "Someone did.",
				Published:       at("2026-07-23T17:55:00Z"),
				Updated:         at("2026-07-23T17:57:00Z"),
				InReplyTo:       "https://alice.example/p/1",
				Source:          &feed.Source{URL: "https://alice.example/rss.xml", Name: "Alice"},
			},
		},
	}
}

func conversationLeaf() *feed.Feed {
	return &feed.Feed{
		Title:       "Replies to a post by Bob",
		Link:        "https://bob.example/p/7",
		Description: "Replies to a post by Bob",
		Updated:     at("2026-07-23T17:52:00Z"),
		Self:        "https://bob.example/p/7/replies.xml",
		Items: []feed.Item{{
			GUID:            "https://alice.example/p/3",
			GUIDIsPermalink: true,
			HTML:            "<p>Three levels is enough to prove it walks.</p>",
			Markdown:        "Three levels is enough to prove it walks.",
			Published:       at("2026-07-23T17:52:00Z"),
			InReplyTo:       "https://bob.example/p/7",
			Source:          &feed.Source{URL: "https://alice.example/rss.xml", Name: "Alice"},
		}},
	}
}

func TestGoldenConversation(t *testing.T) {
	cases := []struct {
		name string
		f    *feed.Feed
	}{
		{"conversation-root.xml", conversationRoot()},
		{"conversation-replies-1.xml", conversationReplies()},
		{"conversation-replies-7.xml", conversationLeaf()},
	}
	for _, c := range cases {
		assertGolden(t, c.name, RSS(c.f, emitOptions()))
	}
}

func TestGoldenPassThrough(t *testing.T) {
	f := &feed.Feed{
		Title: "Relayed",
		Link:  "https://relay.example/",
		Self:  "https://relay.example/rss.xml",
		Unknown: []feed.Element{{
			Name: xml.Name{Space: feed.NSSource, Local: "localTime"},
			Text: "Thu, July 23, 2026 12:15 PM EDT",
		}},
		Items: []feed.Item{{
			GUID:            "https://origin.example/p/9",
			GUIDIsPermalink: true,
			HTML:            "<p>Relayed intact.</p>",
			Published:       at("2026-07-23T12:00:00Z"),
			Unknown: []feed.Element{
				{
					Name: xml.Name{Space: "https://vendor.example/ns", Local: "mood"},
					Attrs: []xml.Attr{
						{Name: xml.Name{Local: "scale"}, Value: "1-10"},
					},
					Text: "7",
				},
				{
					Name: xml.Name{Space: feed.NSDublinCore, Local: "creator"},
					Text: "Someone Else",
				},
				{
					Name: xml.Name{Space: "https://vendor.example/ns", Local: "wrapper"},
					Children: []feed.Element{{
						Name: xml.Name{Space: "https://vendor.example/ns", Local: "inner"},
						Text: "nested",
					}},
				},
			},
		}},
	}
	assertGolden(t, "passthrough.xml", RSS(f, emitOptions()))
}

func TestRoundTripKeepsSourceElements(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", "rsschat-firehose.xml"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := feedin.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	again, err := feedin.Parse(RSS(original, emitOptions()))
	if err != nil {
		t.Fatalf("emitted feed does not parse: %v", err)
	}

	if len(again.Items) != len(original.Items) {
		t.Fatalf("items = %d, want %d", len(again.Items), len(original.Items))
	}
	if again.Self != original.Self {
		t.Errorf("self = %q, want %q", again.Self, original.Self)
	}

	for i := range original.Items {
		a, b := &original.Items[i], &again.Items[i]
		if a.GUID != b.GUID {
			t.Fatalf("item %d guid = %q, want %q", i, b.GUID, a.GUID)
		}
		if a.Markdown != b.Markdown {
			t.Errorf("item %d markdown changed:\n got %q\nwant %q", i, b.Markdown, a.Markdown)
		}
		if a.InReplyTo != b.InReplyTo {
			t.Errorf("item %d inReplyTo = %q, want %q", i, b.InReplyTo, a.InReplyTo)
		}
		if (a.Comments == nil) != (b.Comments == nil) {
			t.Errorf("item %d comments presence changed", i)
			continue
		}
		if a.Comments != nil && *a.Comments != *b.Comments {
			t.Errorf("item %d comments = %+v, want %+v", i, b.Comments, a.Comments)
		}
		if (a.Source == nil) != (b.Source == nil) {
			t.Errorf("item %d source presence changed", i)
			continue
		}
		if a.Source != nil && *a.Source != *b.Source {
			t.Errorf("item %d source = %+v, want %+v", i, b.Source, a.Source)
		}
	}
}

func TestRoundTripKeepsAccountsAndPassThrough(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", "scripting-com.xml"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := feedin.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	again, err := feedin.Parse(RSS(original, emitOptions()))
	if err != nil {
		t.Fatal(err)
	}

	if len(again.Accounts) != len(original.Accounts) {
		t.Fatalf("accounts = %+v, want %+v", again.Accounts, original.Accounts)
	}
	for i := range original.Accounts {
		if again.Accounts[i] != original.Accounts[i] {
			t.Errorf("account %d = %+v, want %+v", i, again.Accounts[i], original.Accounts[i])
		}
	}

	before := unknownNames(original.Unknown)
	after := unknownNames(again.Unknown)
	for name := range before {
		if !after[name] {
			t.Errorf("%s was dropped on re-emission", name)
		}
	}
}

func unknownNames(els []feed.Element) map[string]bool {
	out := map[string]bool{}
	for _, e := range els {
		out[e.Name.Space+" "+e.Name.Local] = true
	}
	return out
}

func TestEmissionIsDeterministic(t *testing.T) {
	f := conversationReplies()
	first := RSS(f, emitOptions())
	for i := 0; i < 20; i++ {
		if !bytes.Equal(RSS(f, emitOptions()), first) {
			t.Fatal("emission is not byte-stable across runs")
		}
	}
}

func TestGUIDPermalinkAttribute(t *testing.T) {
	f := &feed.Feed{
		Title: "Opaque",
		Items: []feed.Item{
			{GUID: "tag:example.org,2026:1", GUIDIsPermalink: false, Link: "https://example.org/p/1"},
			{GUID: "https://example.org/p/2", GUIDIsPermalink: true},
		},
	}
	out := string(RSS(f, RSSOptions{}))

	if !bytes.Contains([]byte(out), []byte(`<guid isPermaLink="false">tag:example.org,2026:1</guid>`)) {
		t.Errorf("opaque guid lost its isPermaLink attribute:\n%s", out)
	}
	if bytes.Contains([]byte(out), []byte(`<guid isPermaLink="true"`)) {
		t.Errorf("permalink guid should not spell out the default:\n%s", out)
	}
}
