package interop

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (i *instance) followAndAskForPush(feedURL string) int64 {
	i.t.Helper()
	ctx := context.Background()

	source := i.follow(feedURL)
	i.read(source)
	i.app.AskForPush(ctx, source)
	return source.ID
}

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAPostTravelsByPushWithoutAnyPolling(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	alice.publish("Before", "Something to make the feed exist.", "")

	sourceID := bob.followAndAskForPush(alice.feedURL())

	waitFor(t, "bob's subscription to be confirmed", func() bool {
		var until int64
		bob.db.Read.QueryRowContext(ctx,
			`select coalesce(hub_lease_until, 0) from source where id = ?`, sourceID).Scan(&until)
		return until > time.Now().Unix()
	})

	var subscribers int
	alice.db.Read.QueryRowContext(ctx,
		`select count(*) from hub_subscriber where verified_at is not null`).Scan(&subscribers)
	if subscribers != 1 {
		t.Fatalf("alice's hub has %d confirmed subscribers, want 1", subscribers)
	}

	alice.publish("Pushed", "This one must arrive without bob asking for it.", "")
	alice.app.Distribute(ctx, alice.feedURL())

	waitFor(t, "the pushed post to reach bob", func() bool {
		items, err := bob.sources.Timeline(ctx, 20, time.Time{})
		if err != nil {
			return false
		}
		for _, item := range items {
			if item.Title == "Pushed" {
				return true
			}
		}
		return false
	})

	var pushedAt int64
	bob.db.Read.QueryRowContext(ctx,
		`select coalesce(last_push_at, 0) from source where id = ?`, sourceID).Scan(&pushedAt)
	if pushedAt == 0 {
		t.Error("the item arrived but was not recorded as a push")
	}
}

func TestOurFeedsAdvertiseTheHubAndTheCloud(t *testing.T) {
	alice := newInstance(t, "alice.test")
	alice.publish("Anything", "So the feed is not empty.", "")

	body := get(t, alice.feedURL())

	for _, want := range []string{
		`rel="hub"`,
		alice.server.URL + "/websub/hub",
		`rel="self"`,
		`<cloud`,
		`path="/rsscloud/pleaseNotify"`,
		`protocol="http-post"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the feed does not advertise %q:\n%s", want, body)
		}
	}
}

func TestTheHubRefusesTopicsItDoesNotPublish(t *testing.T) {
	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	form := "hub.mode=subscribe&hub.topic=https%3A%2F%2Fsomewhere.example%2Ffeed.xml&hub.callback=" +
		bob.server.URL + "%2Fwebsub%2F1"

	resp := postForm(t, alice.server.URL+"/websub/hub", form)
	if resp != 404 {
		t.Errorf("the hub accepted a topic it does not publish: %d", resp)
	}
}

func TestAForgedPushIsRefused(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	alice.publish("Real", "The genuine article.", "")
	sourceID := bob.followAndAskForPush(alice.feedURL())

	waitFor(t, "the subscription to settle", func() bool {
		var until int64
		bob.db.Read.QueryRowContext(ctx,
			`select coalesce(hub_lease_until, 0) from source where id = ?`, sourceID).Scan(&until)
		return until > time.Now().Unix()
	})

	forged := `<?xml version="1.0"?><rss version="2.0"><channel>
		<title>Not alice</title><link>https://alice.test/</link><description>x</description>
		<item><title>Forged</title><guid isPermaLink="true">https://alice.test/p/999</guid></item>
	</channel></rss>`

	status := postSigned(t, bob.server.URL+"/websub/"+itoa(sourceID), forged, "sha256=deadbeef")
	if status >= 300 {
		t.Errorf("a forged delivery got %d; it should be swallowed quietly", status)
	}

	items, err := bob.sources.Timeline(ctx, 20, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Title == "Forged" {
			t.Fatal("a delivery with a bad signature was stored")
		}
	}
}

func postForm(t *testing.T, target, body string) int {
	t.Helper()
	resp, err := http.Post(target, "application/x-www-form-urlencoded", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func postSigned(t *testing.T, target, body, signature string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/rss+xml")
	req.Header.Set("X-Hub-Signature", signature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestRSSCloudRegistersAndPings(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	alice.publish("Before the cloud", "Something to read.", "")

	source := bob.follow(alice.feedURL())
	bob.read(source)
	bob.app.RegisterWithCloud(ctx, source.ID, alice.server.URL+"/rsscloud/pleaseNotify", alice.feedURL())

	waitFor(t, "bob's cloud registration to be confirmed", func() bool {
		var until int64
		bob.db.Read.QueryRowContext(ctx,
			`select coalesce(cloud_until, 0) from source where id = ?`, source.ID).Scan(&until)
		return until > time.Now().Unix()
	})

	var registered int
	alice.db.Read.QueryRowContext(ctx,
		`select count(*) from cloud_subscriber where topic = ?`, alice.feedURL()).Scan(&registered)
	if registered != 1 {
		t.Fatalf("alice's cloud has %d subscribers, want 1", registered)
	}

	alice.publish("After the cloud", "This should be fetched because of a ping.", "")
	alice.app.Distribute(ctx, alice.feedURL())

	waitFor(t, "the ping to make bob re-read the feed", func() bool {
		items, err := bob.sources.Timeline(ctx, 20, time.Time{})
		if err != nil {
			return false
		}
		for _, item := range items {
			if item.Title == "After the cloud" {
				return true
			}
		}
		return false
	})
}

func TestTheCloudRefusesAnAddressThatDoesNotAnswer(t *testing.T) {
	alice := newInstance(t, "alice.test")
	alice.publish("Anything", "So the feed exists.", "")

	form := url.Values{
		"protocol": {"http-post"},
		"domain":   {"127.0.0.1"},
		"port":     {"9"},
		"path":     {"/nowhere"},
		"url1":     {alice.feedURL()},
	}

	resp, err := http.PostForm(alice.server.URL+"/rsscloud/pleaseNotify", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `success="false"`) {
		t.Errorf("the cloud registered an address that never answered: %s", body)
	}

	var registered int
	alice.db.Read.QueryRowContext(context.Background(),
		`select count(*) from cloud_subscriber`).Scan(&registered)
	if registered != 0 {
		t.Errorf("%d unverified subscribers were stored", registered)
	}
}
