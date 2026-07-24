package opml

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseKeepsFolders(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?>
<opml version="2.0">
  <head><title>subscriptions</title></head>
  <body>
    <outline text="Loose" type="rss" xmlUrl="https://loose.example/rss.xml"/>
    <outline text="People">
      <outline title="Dave" type="rss" xmlUrl="https://scripting.com/rss.xml" htmlUrl="https://scripting.com/"/>
      <outline text="Nested">
        <outline text="Deep" type="rss" xmlUrl="https://deep.example/rss.xml"/>
      </outline>
    </outline>
  </body>
</opml>`)

	subs, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("found %d subscriptions, want 3", len(subs))
	}

	byURL := map[string]Subscription{}
	for _, sub := range subs {
		byURL[sub.FeedURL] = sub
	}

	if got := byURL["https://loose.example/rss.xml"]; got.Category != "" {
		t.Errorf("a top-level feed got category %q", got.Category)
	}
	if got := byURL["https://scripting.com/rss.xml"]; got.Category != "People" {
		t.Errorf("category = %q, want People", got.Category)
	}
	if got := byURL["https://scripting.com/rss.xml"]; got.Title != "Dave" || got.SiteURL != "https://scripting.com/" {
		t.Errorf("subscription = %+v", got)
	}
	if got := byURL["https://deep.example/rss.xml"]; got.Category != "People/Nested" {
		t.Errorf("nested category = %q, want People/Nested", got.Category)
	}
}

func TestParseRejectsWhatIsNotAnOutline(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte(``),
		[]byte(`<rss version="2.0"><channel><title>no</title></channel></rss>`),
		[]byte(`<opml version="2.0"><head/><body/></opml>`),
		[]byte(`<opml><body><outline text="folder only"/></body></opml>`),
	} {
		if _, err := Parse(bad); !errors.Is(err, ErrNotOPML) {
			t.Errorf("Parse(%.30q) gave %v, want ErrNotOPML", bad, err)
		}
	}
}

func TestRoundTripPreservesFoldersAndURLs(t *testing.T) {
	original := []Subscription{
		{Title: "Loose", FeedURL: "https://loose.example/rss.xml"},
		{Title: "Dave", FeedURL: "https://scripting.com/rss.xml", SiteURL: "https://scripting.com/", Category: "People"},
		{Title: "Bob", FeedURL: "https://bob.example/rss.xml", Category: "People"},
		{Title: "Deep", FeedURL: "https://deep.example/rss.xml", Category: "People/Nested"},
	}

	rendered, err := Render("subscriptions", "alice", original, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(rendered), `<?xml version="1.0"`) {
		t.Errorf("no xml declaration:\n%s", rendered)
	}

	back, err := Parse(rendered)
	if err != nil {
		t.Fatalf("our own output does not parse: %v\n%s", err, rendered)
	}
	if len(back) != len(original) {
		t.Fatalf("round trip returned %d of %d subscriptions", len(back), len(original))
	}

	got := map[string]Subscription{}
	for _, sub := range back {
		got[sub.FeedURL] = sub
	}
	for _, want := range original {
		have, ok := got[want.FeedURL]
		if !ok {
			t.Errorf("%s did not survive", want.FeedURL)
			continue
		}
		if have.Category != want.Category {
			t.Errorf("%s came back in %q, want %q", want.FeedURL, have.Category, want.Category)
		}
		if have.Title != want.Title {
			t.Errorf("%s came back titled %q, want %q", want.FeedURL, have.Title, want.Title)
		}
	}
}

func TestRenderGroupsEachCategoryOnce(t *testing.T) {
	rendered, err := Render("subs", "alice", []Subscription{
		{FeedURL: "https://a.example/f.xml", Category: "News"},
		{FeedURL: "https://b.example/f.xml", Category: "News"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(rendered), `text="News"`); n != 1 {
		t.Errorf("the News folder appears %d times, want 1:\n%s", n, rendered)
	}
}
