package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testApp(t *testing.T) http.Handler {
	t.Helper()
	db := tempDB(t)
	return NewApp(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "test.example").Handler()
}

func get(t *testing.T, h http.Handler, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

func TestEveryAssetIsVersioned(t *testing.T) {
	for _, name := range []string{"tokens.css", "app.css", "icons/rss.svg", "favicon.ico", "mark.png", "fonts/fraunces-latin.woff2"} {
		a, ok := assets[name]
		if !ok {
			t.Errorf("%s has no version", name)
			continue
		}
		version := a.version
		if len(version) != 10 {
			t.Errorf("%s version = %q, want 10 hex characters", name, version)
		}
		if want := "/assets/" + name + "?v=" + version; assetURL(name) != want {
			t.Errorf("assetURL(%q) = %q, want %q", name, assetURL(name), want)
		}
	}
}

func TestUnknownAssetGetsNoVersion(t *testing.T) {
	if got := assetURL("nope.css"); got != "/assets/nope.css" {
		t.Errorf("assetURL of an unknown file = %q", got)
	}
}

func TestVersionedAssetIsImmutableAndUnversionedRevalidates(t *testing.T) {
	h := testApp(t)

	versioned := get(t, h, assetURL("app.css"))
	if versioned.StatusCode != http.StatusOK {
		t.Fatalf("versioned asset status = %d", versioned.StatusCode)
	}
	if cc := versioned.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset Cache-Control = %q, want immutable", cc)
	}

	bare := get(t, h, "/assets/app.css")
	if cc := bare.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("unversioned asset Cache-Control = %q, want no-cache", cc)
	}

	stale := get(t, h, "/assets/app.css?v=deadbeef00")
	if cc := stale.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("stale version Cache-Control = %q, want no-cache", cc)
	}
}

func TestIconInliningKeepsCurrentColor(t *testing.T) {
	svg := string(icon("rss"))
	if !strings.HasPrefix(svg, `<svg class="icon" aria-hidden="true" focusable="false"`) {
		t.Fatalf("icon markup = %.80s", svg)
	}
	if !strings.Contains(svg, `fill="currentColor"`) {
		t.Error("icon lost fill=currentColor and will not follow the theme")
	}
	if !strings.Contains(svg, "</svg>") {
		t.Error("icon markup is truncated")
	}
	if got := icon("does-not-exist"); got != "" {
		t.Errorf("missing icon returned %q, want empty", got)
	}
}

func TestReaderRendersWithCredits(t *testing.T) {
	resp := get(t, testApp(t), "/dev/preview")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)

	if strings.Contains(page, "{{") {
		t.Error("template actions survived into the rendered page")
	}
	for _, want := range []string{
		"egg.design",
		"cornetespoir",
		"Pixelarticons",
		"Gerrit",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("credit for %q is missing from the page", want)
		}
	}
	if !regexp.MustCompile(`href="/assets/tokens\.css\?v=[0-9a-f]{10}"`).MatchString(page) {
		t.Error("stylesheet link is not versioned")
	}
	if n := strings.Count(page, `<svg class="icon"`); n == 0 {
		t.Error("no icons were inlined")
	}
}

func TestOnlyTheHarmlessProbesAreOpen(t *testing.T) {
	h := testApp(t)

	if resp := get(t, h, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d; an uptime monitor has to reach it", resp.StatusCode)
	}
	body, _ := io.ReadAll(get(t, h, "/healthz").Body)
	if strings.TrimSpace(string(body)) != "ok" {
		t.Errorf("/healthz says %q; it must not carry any detail", body)
	}

	ready, _ := io.ReadAll(get(t, h, "/readyz").Body)
	if strings.Contains(string(ready), "schema") || strings.Contains(string(ready), "database") {
		t.Errorf("/readyz leaks the reason to anyone who asks: %q", ready)
	}

	if resp := get(t, h, "/metrics"); resp.StatusCode == http.StatusOK {
		t.Error("/metrics answered without a token being configured")
	}
	if resp := get(t, h, "/admin/status"); resp.StatusCode == http.StatusOK {
		t.Error("/admin/status answered a signed-out visitor")
	}
}

func TestSecurityHeaders(t *testing.T) {
	resp := get(t, testApp(t), "/")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestReadyzReportsPendingMigrations(t *testing.T) {
	db := tempDB(t)
	admin := New(db, quietLogger(), "test.example", Options{}).Handler()

	if resp := get(t, admin, "/readyz"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz on an unmigrated database = %d, want 503", resp.StatusCode)
	}
	if resp := get(t, admin, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d, want 200 regardless of schema state", resp.StatusCode)
	}

	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resp := get(t, admin, "/readyz"); resp.StatusCode != http.StatusOK {
		t.Errorf("readyz after migrating = %d, want 200", resp.StatusCode)
	}
}

func TestMetricsAreOffWithoutAToken(t *testing.T) {
	h := New(tempDB(t), quietLogger(), "test.example", Options{}).Handler()

	if resp := get(t, h, "/metrics"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("metrics without a configured token = %d, want 404", resp.StatusCode)
	}
}

func TestMetricsNeedTheRightToken(t *testing.T) {
	h := New(tempDB(t), quietLogger(), "test.example", Options{MetricsToken: "s3cret"}).Handler()

	if resp := get(t, h, "/metrics"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("metrics with no token = %d, want 401", resp.StatusCode)
	}
	if resp := get(t, h, "/metrics?token=wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("metrics with a wrong token = %d, want 401", resp.StatusCode)
	}

	resp := get(t, h, "/metrics?token=s3cret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics with the right token = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"rss_expert_build_info", "rss_expert_database_bytes"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("%s is missing from /metrics", want)
		}
	}
}

func TestEveryPageHasATitle(t *testing.T) {
	h := testApp(t)
	for path, want := range map[string]string{
		"/":            "<title>RSS Expert</title>",
		"/login":       "<title>Sign in — RSS Expert</title>",
		"/dev/preview": "<title>Preview — RSS Expert</title>",
	} {
		body, err := io.ReadAll(get(t, h, path).Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s is missing %s", path, want)
		}
	}
}

func TestPagesDeclareTheFavicon(t *testing.T) {
	body, _ := io.ReadAll(get(t, testApp(t), "/").Body)
	page := string(body)
	for _, want := range []string{`rel="icon"`, "favicon.ico?v=", "favicon.png?v=", `rel="apple-touch-icon"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not declare %s", want)
		}
	}
}

func TestEverythingTextIsCompressed(t *testing.T) {
	for _, mediaType := range []string{
		"text/html; charset=utf-8",
		"application/rss+xml; charset=utf-8",
		"application/feed+json",
		"text/x-opml; charset=utf-8",
		"image/svg+xml",
	} {
		if !compressible(mediaType) {
			t.Errorf("%q is text and goes out uncompressed", mediaType)
		}
	}
	for _, mediaType := range []string{"image/jpeg", "font/woff2", "video/mp4", "application/octet-stream"} {
		if compressible(mediaType) {
			t.Errorf("%q is already compressed and would be packed twice", mediaType)
		}
	}
}

func TestFeedsGoOutCompressed(t *testing.T) {
	h := testApp(t)

	req := httptest.NewRequest(http.MethodGet, "/users/rss.xml", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Body.Len() < compressAbove {
		t.Skip("this instance's firehose is too small to be worth compressing")
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("the firehose went out as %q; feeds are the heaviest thing here", got)
	}
}
