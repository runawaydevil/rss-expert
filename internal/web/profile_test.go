package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

func TestPublicProfileShowsNameBioAndPicture(t *testing.T) {
	ctx := context.Background()

	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	app := New(db, quietLogger(), "https://rss.expert", Options{})
	h := app.Handler()

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "pablo@rss.expert", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	posts := publish.NewStore(db, "https://rss.expert")
	handle, err := posts.EnsureHandle(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}

	profile := identity.Profile{
		DisplayName: "Pablo Murad",
		Bio:         "reads and writes over plain RSS",
		AvatarSHA:   "abc123def456",
	}
	if err := accounts.SetProfile(ctx, owner.ID, profile); err != nil {
		t.Fatal(err)
	}

	page, _ := io.ReadAll(get(t, h, "/users/"+handle).Body)
	body := string(page)

	for _, want := range []string{"Pablo Murad", "reads and writes over plain RSS", "/media/abc123def456"} {
		if !strings.Contains(body, want) {
			t.Errorf("the profile page does not show %q:\n%s", want, body)
		}
	}
}

func TestProfileSettingsFormRenders(t *testing.T) {
	ctx := context.Background()
	h, accounts := testAppWithAccounts(t)

	if _, err := accounts.Create(ctx, "owner@test.example", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}
	session := signIn(t, h, "owner@test.example")

	resp := getAs(t, h, "/settings/profile", session)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the profile settings form answered %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Your profile") {
		t.Errorf("the profile settings form did not render:\n%s", body)
	}
}
