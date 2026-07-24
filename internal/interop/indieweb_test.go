package interop

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/micropub"
	"github.com/runawaydevil/rss-expert/internal/webmention"
)

func TestWebmentionTravelsBetweenTwoInstances(t *testing.T) {
	ctx := context.Background()

	alice := newInstance(t, "alice.test")
	bob := newInstance(t, "bob.test")

	opening := alice.publish("A question", "Does a webmention reach the other side?", "")

	bobMentions := webmention.New(bob.db, webmention.Options{
		Domain:            bob.server.URL,
		AllowPrivateAddrs: true,
	})
	aliceMentions := webmention.New(alice.db, webmention.Options{
		Domain:            alice.server.URL,
		AllowPrivateAddrs: true,
	})

	target := opening.GUID
	reply := bob.publish("", "It does. Here is the proof.", target)
	source := reply.GUID

	if _, err := bobMentions.Send(ctx, source, target); err != nil {
		t.Fatalf("bob could not send a webmention: %v", err)
	}

	waiting, err := aliceMentions.Unverified(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 {
		t.Fatalf("alice holds %d unverified mentions, want 1", len(waiting))
	}

	if err := aliceMentions.VerifyIncoming(ctx, waiting[0]); err != nil {
		t.Fatalf("verification failed: %v", err)
	}

	shown, err := aliceMentions.Approved(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 0 {
		t.Fatal("a verified mention is on display before a moderator agreed")
	}

	pending, _ := aliceMentions.AwaitingModeration(ctx, 10)
	if len(pending) != 1 {
		t.Fatalf("%d mentions await moderation", len(pending))
	}
	if err := aliceMentions.Decide(ctx, pending[0].ID, alice.owner.ID, true); err != nil {
		t.Fatal(err)
	}

	shown, _ = aliceMentions.Approved(ctx, target)
	if len(shown) != 1 {
		t.Fatalf("%d mentions shown after approval", len(shown))
	}
	if shown[0].Source != source {
		t.Errorf("source = %q, want %q", shown[0].Source, source)
	}
}

func TestOurPagesAdvertiseTheEndpoints(t *testing.T) {
	alice := newInstance(t, "alice.test")
	post := alice.publish("Hello", "A post that can be answered.", "")

	page := get(t, post.GUID)
	for _, want := range []string{`rel="webmention"`, `rel="micropub"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the post page does not advertise %s; nobody can answer what they cannot find", want)
		}
	}
}

func TestMicropubPublishesAndUpdates(t *testing.T) {
	ctx := context.Background()
	alice := newInstance(t, "alice.test")

	tokens := micropub.New(alice.db)
	token, err := tokens.Issue(ctx, alice.owner, "https://client.example/", "create update delete")
	if err != nil {
		t.Fatal(err)
	}

	endpoint := alice.server.URL + "/micropub"

	resp := postTo(t, endpoint, token, url.Values{
		"h":       {"entry"},
		"name":    {"Written elsewhere"},
		"content": {"Published through Micropub, from another app."},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("create returned no Location")
	}

	feed := get(t, alice.feedURL())
	if !strings.Contains(feed, "Published through Micropub") {
		t.Error("a micropub post did not reach the account feed")
	}

	resp = postJSON(t, endpoint, token, `{
		"action": "update",
		"url": "`+location+`",
		"replace": {"content": ["Corrected through Micropub."]}
	}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update returned %d", resp.StatusCode)
	}

	feed = get(t, alice.feedURL())
	if !strings.Contains(feed, "Corrected through Micropub") {
		t.Error("the micropub edit did not reach the feed")
	}
	if strings.Contains(feed, "from another app") {
		t.Error("the superseded text is still in the feed")
	}

	resp = postJSON(t, endpoint, token, `{"action":"delete","url":"`+location+`"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete returned %d", resp.StatusCode)
	}
	feed = get(t, alice.feedURL())
	if strings.Contains(feed, "Corrected through Micropub") {
		t.Error("a deleted post is still in the feed")
	}
}

func TestMicropubRefusesTheWrongToken(t *testing.T) {
	ctx := context.Background()
	alice := newInstance(t, "alice.test")
	endpoint := alice.server.URL + "/micropub"

	resp := postTo(t, endpoint, "", url.Values{"content": {"no token"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a request with no token returned %d, want 401", resp.StatusCode)
	}

	resp = postTo(t, endpoint, "not-a-real-token", url.Values{"content": {"bad token"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an invented token returned %d, want 401", resp.StatusCode)
	}

	tokens := micropub.New(alice.db)
	readOnly, err := tokens.Issue(ctx, alice.owner, "https://client.example/", "delete")
	if err != nil {
		t.Fatal(err)
	}
	resp = postTo(t, endpoint, readOnly, url.Values{"content": {"wrong scope"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a token without the create scope returned %d, want 403", resp.StatusCode)
	}

	feed := get(t, alice.feedURL())
	for _, forbidden := range []string{"no token", "bad token", "wrong scope"} {
		if strings.Contains(feed, forbidden) {
			t.Errorf("%q was published despite being refused", forbidden)
		}
	}
}

func postTo(t *testing.T, endpoint, token string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func postJSON(t *testing.T, endpoint, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestMicropubUploadsAFileAndCarriesItInTheFeed(t *testing.T) {
	ctx := context.Background()
	alice := newInstance(t, "alice.test")

	tokens := micropub.New(alice.db)
	token, err := tokens.Issue(ctx, alice.owner, "https://client.example/", "create media")
	if err != nil {
		t.Fatal(err)
	}

	config := getWithToken(t, alice.server.URL+"/micropub?q=config", token)
	if !strings.Contains(config, "/micropub/media") {
		t.Fatalf("the config does not advertise a media endpoint:\n%s", config)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(testJPEG(t)); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("alt", "A test pattern"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, alice.server.URL+"/micropub/media", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the upload returned %d", resp.StatusCode)
	}
	fileURL := resp.Header.Get("Location")
	if fileURL == "" {
		t.Fatal("the upload returned no Location")
	}

	created := postJSON(t, alice.server.URL+"/micropub", token, `{
		"type": ["h-entry"],
		"properties": {
			"content": ["A picture taken elsewhere."],
			"photo": ["`+fileURL+`"]
		}
	}`)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d", created.StatusCode)
	}

	feed := get(t, alice.feedURL())
	if !strings.Contains(feed, "<enclosure") {
		t.Fatalf("the uploaded file is not in the feed:\n%s", feed)
	}
	if !strings.Contains(feed, fileURL) {
		t.Error("the enclosure does not point at the file that was uploaded")
	}
}

func getWithToken(t *testing.T, target, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 14))
	for x := 0; x < 20; x++ {
		for y := 0; y < 14; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 6), 70, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
