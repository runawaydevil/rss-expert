package safety

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func localFetcher(o Options) *Fetcher {
	o.AllowPrivateAddrs = true
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	return New(o)
}

func TestGetBlocksLoopbackAddress(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached")
	})

	_, err := New(Options{Timeout: 5 * time.Second}).Get(context.Background(), srv.URL, nil)
	var ae *AddressError
	if !errors.As(err, &ae) {
		t.Fatalf("got %v, want AddressError", err)
	}
}

func TestGetResolvesNameBeforeDialing(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached")
	})
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	target := "http://localhost:" + u.Port() + "/feed.xml"
	_, err = New(Options{Timeout: 5 * time.Second}).Get(context.Background(), target, nil)

	var ae *AddressError
	if !errors.As(err, &ae) {
		t.Fatalf("got %v, want AddressError for a name resolving to loopback", err)
	}
}

func TestGetAllowsPrivateWhenConfigured(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, "<rss/>")
	})

	res, err := localFetcher(Options{}).Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Body); got != "<rss/>" {
		t.Errorf("body = %q", got)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestGetRejectsSchemes(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.org/",
		"ftp://example.org/feed.xml",
		"data:text/xml,<rss/>",
		"jar:http://example.org/!/",
	} {
		_, err := New(Options{}).Get(context.Background(), raw, nil)
		if !errors.Is(err, ErrScheme) {
			t.Errorf("%s: got %v, want ErrScheme", raw, err)
		}
	}
}

func TestGetRejectsURLCredentials(t *testing.T) {
	_, err := New(Options{}).Get(context.Background(), "http://user:pass@example.org/feed.xml", nil)
	if !errors.Is(err, ErrURLCredential) {
		t.Fatalf("got %v, want ErrURLCredential", err)
	}
}

func TestRedirectToBlockedSchemeIsRejected(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "gopher://example.org/", http.StatusFound)
	})

	_, err := localFetcher(Options{}).Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrScheme) {
		t.Fatalf("got %v, want ErrScheme", err)
	}
}

func TestRedirectLimit(t *testing.T) {
	var srv *httptest.Server
	srv = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	})

	_, err := localFetcher(Options{MaxRedirects: 3}).Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("got %v, want ErrTooManyHops", err)
	}
}

func TestRedirectStripsCredentials(t *testing.T) {
	var seen http.Header
	var srv *httptest.Server
	srv = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			seen = r.Header.Clone()
			fmt.Fprint(w, "ok")
			return
		}
		http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer secret")
	header.Set("Cookie", "session=secret")

	if _, err := localFetcher(Options{}).Get(context.Background(), srv.URL, header); err != nil {
		t.Fatal(err)
	}
	if v := seen.Get("Authorization"); v != "" {
		t.Errorf("Authorization survived: %q", v)
	}
	if v := seen.Get("Cookie"); v != "" {
		t.Errorf("Cookie survived: %q", v)
	}
}

func TestSizeLimit(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 4096)))
	})

	_, err := localFetcher(Options{MaxBytes: 1024}).Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

func TestSizeLimitBoundary(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 1024)))
	})

	res, err := localFetcher(Options{MaxBytes: 1024}).Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Body) != 1024 {
		t.Errorf("len(body) = %d, want 1024", len(res.Body))
	}
}

func TestContentTypeFilter(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html/>")
	})

	f := localFetcher(Options{AcceptContentTypes: []string{"application/rss+xml", "application/xml"}})
	_, err := f.Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrContentType) {
		t.Fatalf("got %v, want ErrContentType", err)
	}
}

func TestContentTypeFilterAcceptsWithParameters(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "Application/RSS+XML; charset=UTF-8")
		fmt.Fprint(w, "<rss/>")
	})

	f := localFetcher(Options{AcceptContentTypes: []string{"application/rss+xml"}})
	if _, err := f.Get(context.Background(), srv.URL, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNotModified(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, "<rss/>")
	})

	f := localFetcher(Options{})
	first, err := f.Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ETag() != `"v1"` {
		t.Fatalf("etag = %q", first.ETag())
	}

	header := http.Header{}
	header.Set("If-None-Match", first.ETag())
	second, err := f.Get(context.Background(), srv.URL, header)
	if err != nil {
		t.Fatal(err)
	}
	if !second.NotModified() {
		t.Errorf("status = %d, want 304", second.StatusCode)
	}
	if len(second.Body) != 0 {
		t.Errorf("304 carried a body of %d bytes", len(second.Body))
	}
}

func TestFinalURLAfterRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			fmt.Fprint(w, "<rss/>")
			return
		}
		http.Redirect(w, r, srv.URL+"/moved", http.StatusMovedPermanently)
	})

	res, err := localFetcher(Options{}).Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.URL.Path != "/moved" {
		t.Errorf("final url = %s, want path /moved", res.URL)
	}
}

func FuzzCheckURL(f *testing.F) {
	seeds := []string{
		"http://example.org/feed.xml",
		"https://user:pass@example.org/",
		"gopher://example.org/",
		"http://[::1]/",
		"//example.org/",
		"http:///",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		u, err := url.Parse(s)
		if err != nil {
			return
		}
		if CheckURL(u) != nil {
			return
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("scheme %q passed CheckURL", u.Scheme)
		}
		if u.User != nil {
			t.Fatalf("url with credentials passed CheckURL: %s", s)
		}
		if u.Hostname() == "" {
			t.Fatalf("hostless url passed CheckURL: %s", s)
		}
	})
}
