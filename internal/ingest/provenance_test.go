package ingest

import (
	"context"
	"testing"
	"time"
)

func TestAFederatedActorIsProvenanceAndNotASubscription(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	subscribed, err := s.AddSource(ctx, "https://example.com/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.EnsureFederatedSource(ctx, "https://mastodon.example/users/bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}

	if actor.Protocol != ProtocolActivityPub {
		t.Errorf("the actor was stored as protocol %q", actor.Protocol)
	}
	if actor.Subscribed() {
		t.Error("an actor answering a post counts as a subscription")
	}
	if !subscribed.Subscribed() {
		t.Error("a feed somebody added is not counted as a subscription")
	}

	listed, err := s.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != subscribed.ID {
		t.Fatalf("the sources list holds %d entries, want only the feed", len(listed))
	}
}

func TestAFederatedActorIsNeverPolled(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.AddSource(ctx, "https://example.com/feed.xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureFederatedSource(ctx, "https://mastodon.example/users/bob", "Bob"); err != nil {
		t.Fatal(err)
	}

	due, err := s.Due(ctx, time.Now().Add(365*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range due {
		if source.Protocol != ProtocolFeed {
			t.Errorf("the poller was handed %q, which is not a feed", source.FeedURL)
		}
	}
	if len(due) != 1 {
		t.Fatalf("%d sources came due, want only the feed", len(due))
	}
}

func TestFollowingAnActorTwiceKeepsOneRecord(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first, err := s.EnsureFederatedSource(ctx, "https://mastodon.example/users/bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureFederatedSource(ctx, "https://mastodon.example/users/bob", "Bob Again")
	if err != nil {
		t.Fatal(err)
	}

	if first.ID != second.ID {
		t.Errorf("a second reply from the same actor made a new source (%d then %d)", first.ID, second.ID)
	}
	if second.Title != "Bob Again" {
		t.Errorf("the actor's name did not follow the latest delivery: %q", second.Title)
	}
}

func TestALocalFeedIsNotASubscriptionEither(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	local, err := s.EnsureLocalSource(ctx, "https://rss.expert/users/pablo/rss.xml", "pablo")
	if err != nil {
		t.Fatal(err)
	}
	if local.Subscribed() {
		t.Error("this instance's own feed counts as a subscription")
	}
}
