package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-social/internal/store"
)

func testApp(t *testing.T) http.Handler {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewApp(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "test.example").Handler()
}

func get(t *testing.T, h http.Handler, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

func TestEveryAssetIsVersioned(t *testing.T) {
	for _, name := range []string{"tokens.css", "app.css", "icons/rss.svg", "fonts/fraunces-latin.woff2"} {
		version, ok := assetVersion[name]
		if !ok {
			t.Errorf("%s has no version", name)
			continue
		}
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

func TestSpecimenRendersWithCredits(t *testing.T) {
	resp := get(t, testApp(t), "/dev/specimen")
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

func TestAdminEndpointsAreNotOnThePublicPort(t *testing.T) {
	h := testApp(t)
	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		if resp := get(t, h, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d on the app port; it belongs to the admin listener", path, resp.StatusCode)
		}
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

func TestAdminReadyzReportsPendingMigrations(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	admin := NewAdmin(db, "test").Handler()

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

func TestMetricsExposesOurGauges(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	resp := get(t, NewAdmin(db, "test").Handler(), "/metrics")
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"rss_social_build_info", "rss_social_database_bytes"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("%s is missing from /metrics", want)
		}
	}
}
