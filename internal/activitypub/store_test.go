package activitypub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T) (*Store, int64) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-ap")
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

	owner, err := identity.NewStore(db).
		Create(ctx, "owner@rss.expert", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	return New(db), owner.ID
}

func remote(host string) *Actor {
	uri := "https://" + host + "/users/someone"
	return &Actor{
		ID:                uri,
		Type:              "Person",
		PreferredUsername: "someone",
		Inbox:             uri + "/inbox",
		PublicKey: &PublicKey{
			ID:           uri + "#main-key",
			Owner:        uri,
			PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n",
		},
	}
}

func TestAnAccountKeepsTheSameKeyForever(t *testing.T) {
	ap, account := testStore(t)
	ctx := context.Background()

	first, firstPublic, err := ap.EnsureKey(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	second, secondPublic, err := ap.EnsureKey(ctx, account)
	if err != nil {
		t.Fatal(err)
	}

	if !first.Equal(second) {
		t.Error("the account was given a second private key")
	}
	if firstPublic != secondPublic || firstPublic == "" {
		t.Error("the published key changed between calls")
	}
}

func TestASharedInboxIsPreferredForDelivery(t *testing.T) {
	ap, account := testStore(t)
	ctx := context.Background()

	plain := remote("plain.example")
	shared := remote("shared.example")
	shared.Endpoints = &Endpoints{SharedInbox: "https://shared.example/inbox"}

	for _, actor := range []*Actor{plain, shared} {
		if err := ap.AddFollower(ctx, account, actor); err != nil {
			t.Fatal(err)
		}
	}

	inboxes, err := ap.Followers(ctx, account)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"https://plain.example/users/someone/inbox": true,
		"https://shared.example/inbox":              true,
	}
	if len(inboxes) != len(want) {
		t.Fatalf("delivery targets = %v", inboxes)
	}
	for _, inbox := range inboxes {
		if !want[inbox] {
			t.Errorf("unexpected delivery target %q", inbox)
		}
	}
}

func TestFollowingTwiceCountsOnce(t *testing.T) {
	ap, account := testStore(t)
	ctx := context.Background()

	actor := remote("plain.example")
	for range 3 {
		if err := ap.AddFollower(ctx, account, actor); err != nil {
			t.Fatal(err)
		}
	}

	total, err := ap.CountFollowers(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("following three times counted %d followers", total)
	}

	if err := ap.RemoveFollower(ctx, account, actor.ID); err != nil {
		t.Fatal(err)
	}
	if total, _ := ap.CountFollowers(ctx, account); total != 0 {
		t.Errorf("after an undo the follower count is %d", total)
	}
}

func TestAnActivityIsOnlyAcceptedOnce(t *testing.T) {
	ap, _ := testStore(t)
	ctx := context.Background()

	if ap.AlreadySeen(ctx, "https://m.example/activities/1") {
		t.Error("a first delivery was taken for a replay")
	}
	if !ap.AlreadySeen(ctx, "https://m.example/activities/1") {
		t.Error("a replay went through")
	}
	if ap.AlreadySeen(ctx, "") {
		t.Error("an activity with no id was taken for a replay")
	}
}

func TestOldActivitiesAreForgotten(t *testing.T) {
	ap, _ := testStore(t)
	ctx := context.Background()

	ap.AlreadySeen(ctx, "https://m.example/activities/2")

	removed, err := ap.ForgetOldActivities(ctx, -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("forgot %d activities", removed)
	}
}

func TestACachedActorGoesStale(t *testing.T) {
	ap, _ := testStore(t)
	ctx := context.Background()

	actor := remote("plain.example")
	if err := ap.RememberActor(ctx, actor); err != nil {
		t.Fatal(err)
	}

	back, ok := ap.CachedActor(ctx, actor.ID)
	if !ok {
		t.Fatal("a freshly cached actor was not found")
	}
	if err := back.usable(); err != nil {
		t.Errorf("the cached actor came back unusable: %v", err)
	}

	_, err := ap.db.Write.ExecContext(ctx,
		`update remote_actor set fetched_at = ? where actor = ?`,
		time.Now().Add(-2*ActorTTL).Unix(), actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ap.CachedActor(ctx, actor.ID); ok {
		t.Error("a stale actor was served from the cache")
	}
}

func TestAnActorWithNoHostIsNotCached(t *testing.T) {
	ap, _ := testStore(t)

	actor := remote("plain.example")
	actor.ID = "not a url"
	if err := ap.RememberActor(context.Background(), actor); err == nil {
		t.Error("an actor with no host was cached")
	}
}
