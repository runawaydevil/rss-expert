package web

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
)

func sampleJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 12))
	for x := 0; x < 16; x++ {
		for y := 0; y < 12; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 8), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadFile(t *testing.T, h http.Handler, path, csrf, filename string, body []byte, alt string, cookies []*http.Cookie) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	if csrf != "" {
		if err := form.WriteField("csrf", csrf); err != nil {
			t.Fatal(err)
		}
	}
	if alt != "" {
		if err := form.WriteField("alt", alt); err != nil {
			t.Fatal(err)
		}
	}
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", form.FormDataContentType())
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func libraryForm(t *testing.T, h http.Handler, session *http.Cookie) (string, []*http.Cookie) {
	t.Helper()
	resp := getAs(t, h, "/settings/media", session)
	body, _ := io.ReadAll(resp.Body)

	m := csrfInput.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf field in the media library:\n%s", body)
	}
	return string(m[1]), append(resp.Cookies(), session)
}

func TestTheLibraryIsPrivate(t *testing.T) {
	h, _ := testAppWithAccounts(t)

	if resp := getAs(t, h, "/settings/media", nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a signed-out visitor got %d for the library, want a redirect to sign in", resp.StatusCode)
	}
}

func TestUploadThenServe(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	session := signedInAs(t, h, accounts, "alice@example.org", identity.RoleUser)
	csrf, cookies := libraryForm(t, h, session)

	resp := uploadFile(t, h, "/settings/media", csrf, "holiday.jpg", sampleJPEG(t), "A test pattern", cookies)
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload gave %d: %s", resp.StatusCode, body)
	}

	page, _ := io.ReadAll(getAs(t, h, "/settings/media", session).Body)
	if !strings.Contains(string(page), "A test pattern") {
		t.Error("the library does not show the description that was given")
	}

	url := regexp.MustCompile(`/media/[0-9a-f]{64}`).FindString(string(page))
	if url == "" {
		t.Fatalf("no file link in the library:\n%s", page)
	}

	served := getAs(t, h, url, session)
	if served.StatusCode != http.StatusOK {
		t.Fatalf("serving the file gave %d", served.StatusCode)
	}
	if got := served.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := served.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("a stored file is served without nosniff: %q", got)
	}
	if csp := served.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("a stored file is served without a sandbox: %q", csp)
	}
}

func TestUploadWithoutTheFormTokenIsRefused(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	session := signedInAs(t, h, accounts, "alice@example.org", identity.RoleUser)

	resp := uploadFile(t, h, "/settings/media", "", "holiday.jpg", sampleJPEG(t), "", []*http.Cookie{session})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); !strings.Contains(location, "problem=") {
		t.Errorf("a forged upload was accepted quietly: %q", location)
	}

	page, _ := io.ReadAll(getAs(t, h, "/settings/media", session).Body)
	if strings.Contains(string(page), "/media/") {
		t.Error("a file landed in the library without a valid form token")
	}
}

func TestAScriptCannotBeUploadedAsAFile(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	session := signedInAs(t, h, accounts, "alice@example.org", identity.RoleUser)
	csrf, cookies := libraryForm(t, h, session)

	resp := uploadFile(t, h, "/settings/media", csrf, "run.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "", cookies)

	if location := resp.Header.Get("Location"); !strings.Contains(location, "problem=") {
		t.Errorf("an svg carrying script was accepted: %q", location)
	}
}

func TestAttachedFileTravelsInTheFeed(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	session := signedInAs(t, h, accounts, "alice@example.org", identity.RoleUser)
	csrf, cookies := libraryForm(t, h, session)

	if resp := uploadFile(t, h, "/settings/media", csrf, "sound.jpg", sampleJPEG(t), "A test pattern", cookies); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload gave %d", resp.StatusCode)
	}

	writePage := getAs(t, h, "/write", session)
	page, _ := io.ReadAll(writePage.Body)
	id := regexp.MustCompile(`name="media" value="(\d+)"`).FindSubmatch(page)
	if id == nil {
		t.Fatalf("the write form does not offer the uploaded file:\n%s", page)
	}

	writeCSRF := csrfInput.FindSubmatch(page)
	if writeCSRF == nil {
		t.Fatal("no csrf field in the write form")
	}

	resp := postForm(t, h, "/write", map[string][]string{
		"csrf":     {string(writeCSRF[1])},
		"markdown": {"Here is a picture."},
		"media":    {string(id[1])},
	}, append(writePage.Cookies(), session))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("publishing gave %d", resp.StatusCode)
	}

	feed, _ := io.ReadAll(get(t, h, "/users/alice/rss.xml").Body)
	if !strings.Contains(string(feed), "<enclosure") {
		t.Fatalf("the attached file is not in the feed:\n%s", feed)
	}
	if !strings.Contains(string(feed), `type="image/jpeg"`) {
		t.Error("the enclosure does not declare its type")
	}

	post, _ := io.ReadAll(getAs(t, h, "/p/1", session).Body)
	if !strings.Contains(string(post), `alt="A test pattern"`) {
		t.Error("the post does not carry the description to a screen reader")
	}

	timeline, _ := io.ReadAll(getAs(t, h, "/", session).Body)
	if !strings.Contains(string(timeline), "Open the image") ||
		!regexp.MustCompile(`/media/[0-9a-f]{64}`).Match(timeline) {
		t.Fatal("the attachment was not projected back into the shared timeline")
	}
}
