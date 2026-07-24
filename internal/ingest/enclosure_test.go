package ingest

import (
	"context"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/feedin"
)

const podcast = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
	<title>A show</title>
	<link>https://show.example/</link>
	<description>Episodes</description>
	<item>
		<title>Episode one</title>
		<link>https://show.example/1</link>
		<guid isPermaLink="true">https://show.example/1</guid>
		<pubDate>Mon, 20 Jul 2026 10:00:00 GMT</pubDate>
		<enclosure url="https://show.example/1.mp3" length="4194304" type="audio/mpeg"/>
	</item>
	<item>
		<title>Just words</title>
		<link>https://show.example/2</link>
		<guid isPermaLink="true">https://show.example/2</guid>
		<pubDate>Mon, 20 Jul 2026 11:00:00 GMT</pubDate>
	</item>
</channel>
</rss>`

func TestAnEnclosureSurvivesIngestion(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	source, err := s.AddSource(ctx, "https://show.example/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := feedin.Parse([]byte(podcast))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, source, []byte(podcast), "application/rss+xml", parsed); err != nil {
		t.Fatal(err)
	}

	episode, err := s.Item(ctx, "https://show.example/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(episode.Enclosures) != 1 {
		t.Fatalf("%d enclosures on the episode, want 1", len(episode.Enclosures))
	}
	if episode.Enclosures[0].URL != "https://show.example/1.mp3" {
		t.Errorf("enclosure url = %q", episode.Enclosures[0].URL)
	}
	if episode.Enclosures[0].Type != "audio/mpeg" {
		t.Errorf("enclosure type = %q", episode.Enclosures[0].Type)
	}
	if episode.Enclosures[0].Length != 4194304 {
		t.Errorf("enclosure length = %d", episode.Enclosures[0].Length)
	}
	if len(episode.Playable()) != 1 {
		t.Error("an audio enclosure is not offered as playable")
	}

	plain, err := s.Item(ctx, "https://show.example/2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Enclosures) != 0 {
		t.Errorf("an item with no enclosure came back with %d", len(plain.Enclosures))
	}
}
