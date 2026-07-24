package feedin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/feed"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseFile(t *testing.T, name string) *feed.Feed {
	t.Helper()
	f, err := Parse(load(t, name))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func TestRSSChatFirehose(t *testing.T) {
	f := parseFile(t, "rsschat-firehose.xml")

	if f.Title != "rss.chat: all posts" {
		t.Errorf("title = %q", f.Title)
	}
	if f.Self != "https://rss.chat/users/rss.xml" {
		t.Errorf("self = %q", f.Self)
	}
	if len(f.Items) != 100 {
		t.Fatalf("items = %d, want 100", len(f.Items))
	}
	if !f.IsTextcast() {
		t.Error("firehose was not detected as a textcast")
	}

	var withMarkdown, withReply, withComments int
	for i := range f.Items {
		it := &f.Items[i]
		if it.HasMarkdown() {
			withMarkdown++
		}
		if it.IsReply() {
			withReply++
		}
		if it.Comments != nil && it.Comments.FeedURL != "" {
			withComments++
		}
	}
	if withMarkdown != 94 {
		t.Errorf("items with source:markdown = %d, want 94", withMarkdown)
	}
	if withReply != 65 {
		t.Errorf("items with source:inReplyTo = %d, want 65", withReply)
	}
	if withComments != 50 {
		t.Errorf("items with source:comments = %d, want 50", withComments)
	}

	first := f.Items[0]
	if first.GUID != "https://rss.chat/?id=385" {
		t.Errorf("first guid = %q", first.GUID)
	}
	if !first.GUIDIsPermalink {
		t.Error("first guid should default to isPermaLink=true")
	}
	if first.Markdown == "" {
		t.Error("first item lost its markdown")
	}
	if first.Published.IsZero() {
		t.Error("first item lost its pubDate")
	}
}

func TestScriptingComAccounts(t *testing.T) {
	f := parseFile(t, "scripting-com.xml")

	if f.Title != "Scripting News" {
		t.Errorf("title = %q", f.Title)
	}
	want := []feed.Account{
		{Service: "bluesky", Handle: "@scripting.com"},
		{Service: "mastodon", Handle: "@davew@mastodon.social"},
		{Service: "twitter", Handle: "bullmancuso"},
	}
	if len(f.Accounts) != len(want) {
		t.Fatalf("accounts = %+v, want %+v", f.Accounts, want)
	}
	for i, a := range want {
		if f.Accounts[i] != a {
			t.Errorf("account %d = %+v, want %+v", i, f.Accounts[i], a)
		}
	}
	if !f.IsTextcast() {
		t.Error("scripting.com was not detected as a textcast")
	}
}

func TestPassThroughKeepsUnmodelledElements(t *testing.T) {
	f := parseFile(t, "scripting-com.xml")

	var found []string
	for _, e := range f.Unknown {
		if e.Name.Space == feed.NSSource {
			found = append(found, e.Name.Local)
		}
	}
	seen := map[string]bool{}
	for _, n := range found {
		seen[n] = true
	}
	for _, want := range []string{"localTime", "blogroll"} {
		if !seen[want] {
			t.Errorf("source:%s was dropped instead of passed through (kept: %v)", want, found)
		}
	}
	if seen["account"] || seen["self"] {
		t.Error("consumed elements must not also be passed through")
	}
}

func TestLegacyNamespaceIsAccepted(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?>
<rss version="2.0" xmlns:source="http://source.scripting.com/">
  <channel>
    <title>Legacy</title>
    <link>https://example.org/</link>
    <source:self>https://example.org/rss.xml</source:self>
    <source:account service="mastodon">@a@example.org</source:account>
    <item>
      <guid>https://example.org/p/1</guid>
      <description>hi</description>
      <source:markdown>**hi**</source:markdown>
      <source:inReplyTo>https://other.example/p/9</source:inReplyTo>
      <source:comments count="3" feedUrl="https://example.org/p/1/replies.xml"/>
    </item>
  </channel>
</rss>`)

	f, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if f.Self != "https://example.org/rss.xml" {
		t.Errorf("self = %q, legacy http namespace was not normalised", f.Self)
	}
	if len(f.Accounts) != 1 {
		t.Fatalf("accounts = %+v", f.Accounts)
	}
	it := f.Items[0]
	if it.Markdown != "**hi**" {
		t.Errorf("markdown = %q", it.Markdown)
	}
	if it.InReplyTo != "https://other.example/p/9" {
		t.Errorf("inReplyTo = %q", it.InReplyTo)
	}
	if it.Comments == nil || it.Comments.Count != 3 ||
		it.Comments.FeedURL != "https://example.org/p/1/replies.xml" {
		t.Errorf("comments = %+v", it.Comments)
	}
}

func TestThreadingFallbackToRFC4685(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?>
<rss version="2.0" xmlns:thr="http://purl.org/syndication/thread/1.0">
  <channel>
    <title>Threaded</title>
    <item>
      <guid>https://example.org/p/2</guid>
      <description>reply</description>
      <thr:in-reply-to ref="https://example.org/p/1"/>
    </item>
  </channel>
</rss>`)

	f, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Items[0].InReplyTo; got != "https://example.org/p/1" {
		t.Errorf("inReplyTo = %q, want the thr:in-reply-to ref", got)
	}
}

func TestGUIDNotPermalink(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Opaque guids</title>
    <item>
      <guid isPermaLink="false">tag:example.org,2026:1</guid>
      <link>https://example.org/p/1</link>
      <description>x</description>
    </item>
  </channel>
</rss>`)

	f, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	it := f.Items[0]
	if it.GUIDIsPermalink {
		t.Error("isPermaLink=\"false\" was ignored")
	}
	if it.GUID != "tag:example.org,2026:1" {
		t.Errorf("guid = %q", it.GUID)
	}
	if it.Link != "https://example.org/p/1" {
		t.Errorf("link = %q", it.Link)
	}
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"rsschat-firehose.xml", "scripting-com.xml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", name))
		if err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`<rss version="2.0"><channel><item><guid>x</guid></item></channel></rss>`))
	f.Add([]byte(`{"version":"https://jsonfeed.org/version/1.1","items":[]}`))
	f.Add([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>x</id></entry></feed>`))

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		if parsed == nil {
			t.Fatal("Parse returned nil feed and nil error")
		}
		for i := range parsed.Items {
			_ = parsed.Items[i].Key()
		}
	})
}
