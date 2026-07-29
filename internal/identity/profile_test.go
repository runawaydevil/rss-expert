package identity

import (
	"context"
	"strings"
	"testing"
)

func TestProfileRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	account, err := s.Create(ctx, "pablo@rss.expert", "a long enough password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	blank, err := s.Profile(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !blank.Empty() {
		t.Fatalf("a fresh account should have an empty profile, got %+v", blank)
	}

	want := Profile{DisplayName: "Pablo", Bio: "reads and writes over plain RSS", AvatarSHA: "abc123", BannerSHA: "def456"}
	if err := s.SetProfile(ctx, account.ID, want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Profile(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("profile round trip: got %+v, want %+v", got, want)
	}
}

func TestProfileClampsLongText(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	account, err := s.Create(ctx, "long@rss.expert", "a long enough password", RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetProfile(ctx, account.ID, Profile{
		DisplayName: strings.Repeat("n", MaxDisplayName+50),
		Bio:         strings.Repeat("b", MaxBio+200),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Profile(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(got.DisplayName)) != MaxDisplayName {
		t.Errorf("display name kept %d runes, want %d", len([]rune(got.DisplayName)), MaxDisplayName)
	}
	if len([]rune(got.Bio)) != MaxBio {
		t.Errorf("bio kept %d runes, want %d", len([]rune(got.Bio)), MaxBio)
	}
}
