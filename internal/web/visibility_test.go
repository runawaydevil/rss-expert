package web

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

func visibilityFixture(t *testing.T) (http.Handler, *publish.Store, string, *identity.Account) {
	t.Helper()
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := New(db, quietLogger(), "https://rss.expert", Options{}).Handler()

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "owner@rss.expert", testPassword, identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	posts := publish.NewStore(db, "https://rss.expert")
	handle, err := posts.EnsureHandle(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	return h, posts, handle, owner
}

func TestPrivatePostNeverLeaves(t *testing.T) {
	ctx := context.Background()
	h, posts, handle, owner := visibilityFixture(t)

	public, err := posts.CreateVisible(ctx, owner, "PublicHeadline", "shown to all", "", publish.VisibilityPublic)
	if err != nil {
		t.Fatal(err)
	}
	private, err := posts.CreateVisible(ctx, owner, "PrivateHeadline", "only for me", "", publish.VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}

	feeds := map[string]string{
		"account feed": "/users/" + handle + "/rss.xml",
		"firehose":     "/users/rss.xml",
	}
	for name, path := range feeds {
		body, _ := io.ReadAll(get(t, h, path).Body)
		if !strings.Contains(string(body), "PublicHeadline") {
			t.Errorf("%s dropped the public post", name)
		}
		if strings.Contains(string(body), "PrivateHeadline") {
			t.Errorf("%s LEAKED the private post:\n%s", name, body)
		}
	}

	if code := get(t, h, "/p/"+strconv.FormatInt(private.ID, 10)).StatusCode; code != http.StatusNotFound {
		t.Errorf("a stranger got the private post page: status %d", code)
	}
	if code := get(t, h, "/p/"+strconv.FormatInt(public.ID, 10)).StatusCode; code != http.StatusOK {
		t.Errorf("the public post page answered %d to a stranger", code)
	}
}

func TestPrivatePostIsVisibleToItsAuthor(t *testing.T) {
	ctx := context.Background()
	h, posts, _, owner := visibilityFixture(t)

	private, err := posts.CreateVisible(ctx, owner, "SecretHeadline", "for my eyes", "", publish.VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}

	session := signIn(t, h, "owner@rss.expert")
	resp := getAs(t, h, "/p/"+strconv.FormatInt(private.ID, 10), session)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the author could not open their own private post: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SecretHeadline") {
		t.Errorf("the author's private post did not render:\n%s", body)
	}
	if !strings.Contains(string(body), "visibility-badge") {
		t.Errorf("the private post is not marked as such for its author")
	}
}

func TestFollowersOnlyPostStaysOutOfPublicFeeds(t *testing.T) {
	ctx := context.Background()
	h, posts, handle, owner := visibilityFixture(t)

	followers, err := posts.CreateVisible(ctx, owner, "FollowersHeadline", "for my followers", "", publish.VisibilityFollowers)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(get(t, h, "/users/"+handle+"/rss.xml").Body)
	if strings.Contains(string(body), "FollowersHeadline") {
		t.Errorf("a followers-only post leaked into the public RSS feed:\n%s", body)
	}
	if code := get(t, h, "/p/"+strconv.FormatInt(followers.ID, 10)).StatusCode; code != http.StatusNotFound {
		t.Errorf("a stranger reached a followers-only post: status %d", code)
	}
}
