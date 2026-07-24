package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

const testPassword = "a long enough password"

func testAppWithAccounts(t *testing.T) (http.Handler, *identity.Store) {
	t.Helper()
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	app := NewApp(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "test.example")
	return app.Handler(), identity.NewStore(db)
}

var csrfInput = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func loginForm(t *testing.T, h http.Handler) (token string, cookies []*http.Cookie) {
	t.Helper()
	resp := get(t, h, "/login")
	body, _ := io.ReadAll(resp.Body)

	m := csrfInput.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf field in the login form:\n%s", body)
	}
	return string(m[1]), resp.Cookies()
}

func postForm(t *testing.T, h http.Handler, path string, form url.Values, cookies []*http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func signIn(t *testing.T, h http.Handler, email string) *http.Cookie {
	t.Helper()
	token, cookies := loginForm(t, h)

	resp := postForm(t, h, "/login", url.Values{
		"csrf":     {token},
		"email":    {email},
		"password": {testPassword},
	}, cookies)

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("sign-in returned %d:\n%s", resp.StatusCode, body)
	}
	session := cookieNamed(resp.Cookies(), sessionCookieName)
	if session == nil {
		t.Fatal("no session cookie was set")
	}
	return session
}

func TestLoggedOutReaderOffersLogin(t *testing.T) {
	h, _ := testAppWithAccounts(t)

	body, _ := io.ReadAll(get(t, h, "/").Body)
	page := string(body)

	if !strings.Contains(page, `href="/login"`) {
		t.Error("the logged-out reader has no way to sign in")
	}
	if strings.Contains(page, "/logout") {
		t.Error("a signed-out visitor was offered a sign-out control")
	}
	if strings.Contains(page, "/reply?to=") {
		t.Error("a signed-out visitor was offered reply controls")
	}
}

func TestSignInThenReaderShowsTheAccount(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "owner@example.org", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}

	session := signIn(t, h, "owner@example.org")

	req := httptest.NewRequest(http.MethodGet, "/dev/preview", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	page := rec.Body.String()
	for _, want := range []string{"owner@example.org", "/logout", "/reply?to=", "Administration"} {
		if !strings.Contains(page, want) {
			t.Errorf("signed-in page is missing %q", want)
		}
	}
	if strings.Contains(page, `class="button-primary" href="/login"`) {
		t.Error("a signed-in reader was still offered the log-in button")
	}
}

func TestNonAdminDoesNotSeeAdministration(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "reader@example.org", testPassword, identity.RoleUser); err != nil {
		t.Fatal(err)
	}

	session := signIn(t, h, "reader@example.org")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "Administration") {
		t.Error("a plain user was shown the administration link")
	}
}

func TestSessionCookieIsHardened(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "owner@example.org", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}

	session := signIn(t, h, "owner@example.org")
	if !session.HttpOnly {
		t.Error("session cookie is readable from JavaScript")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v", session.SameSite)
	}
	if session.Path != "/" {
		t.Errorf("session cookie path = %q", session.Path)
	}
	if strings.Contains(session.Value, "@") || len(session.Value) < 40 {
		t.Errorf("session cookie value looks guessable: %q", session.Value)
	}
}

func TestWrongPasswordDoesNotStartASession(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "owner@example.org", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}

	token, cookies := loginForm(t, h)
	resp := postForm(t, h, "/login", url.Values{
		"csrf":     {token},
		"email":    {"owner@example.org"},
		"password": {"not the password"},
	}, cookies)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want the form back with 200", resp.StatusCode)
	}
	if cookieNamed(resp.Cookies(), sessionCookieName) != nil {
		t.Fatal("a session cookie was set for a failed sign-in")
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "do not match") {
		t.Errorf("no failure message shown:\n%s", body)
	}
	if strings.Contains(string(body), "no such account") {
		t.Error("the page distinguishes an unknown address from a wrong password")
	}
}

func TestLoginWithoutCSRFIsRejected(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "owner@example.org", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}

	_, cookies := loginForm(t, h)

	for name, form := range map[string]url.Values{
		"missing": {"email": {"owner@example.org"}, "password": {testPassword}},
		"wrong":   {"csrf": {"nope"}, "email": {"owner@example.org"}, "password": {testPassword}},
	} {
		resp := postForm(t, h, "/login", form, cookies)
		if cookieNamed(resp.Cookies(), sessionCookieName) != nil {
			t.Errorf("%s csrf token still signed the visitor in", name)
		}
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "owner@example.org", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}

	token, cookies := loginForm(t, h)
	resp := postForm(t, h, "/login", url.Values{
		"csrf": {token}, "email": {"owner@example.org"}, "password": {testPassword},
	}, cookies)
	session := cookieNamed(resp.Cookies(), sessionCookieName)

	out := postForm(t, h, "/logout", url.Values{"csrf": {token}}, append(cookies, session))
	if out.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout returned %d", out.StatusCode)
	}
	if cleared := cookieNamed(out.Cookies(), sessionCookieName); cleared == nil || cleared.MaxAge >= 0 {
		t.Error("the session cookie was not cleared")
	}

	if _, err := accounts.Session(context.Background(), session.Value); err == nil {
		t.Error("the session still resolves after signing out")
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	if _, err := accounts.Create(context.Background(), "owner@example.org", testPassword, identity.RoleOwner); err != nil {
		t.Fatal(err)
	}

	token, cookies := loginForm(t, h)
	form := url.Values{"csrf": {token}, "email": {"owner@example.org"}, "password": {"wrong"}}

	var limited bool
	for i := 0; i < 12; i++ {
		if postForm(t, h, "/login", form, cookies).StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("twelve failed sign-ins in a row were never rate limited")
	}
}

func TestScriptRunsOnlyFromThisOrigin(t *testing.T) {
	h, _ := testAppWithAccounts(t)
	csp := get(t, h, "/").Header.Get("Content-Security-Policy")

	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("the live island needs script-src 'self': %q", csp)
	}
	for _, loose := range []string{"unsafe-inline", "unsafe-eval", "https:", "*"} {
		if strings.Contains(csp, loose) {
			t.Errorf("the CSP loosened to %q: %q", loose, csp)
		}
	}

	for _, path := range []string{"/", "/dev/preview", "/login", "/rules"} {
		body, _ := io.ReadAll(get(t, h, path).Body)
		for _, tag := range strings.Split(string(body), "<script")[1:] {
			if !strings.HasPrefix(strings.TrimSpace(tag), "src=") {
				t.Errorf("%s carries an inline script; only src'd scripts are allowed:\n<script%.80s", path, tag)
			}
		}
	}
}

func tempDB(t *testing.T) *store.DB {
	t.Helper()

	dir, err := os.MkdirTemp("", "rss-expert-test")
	if err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	return db
}

func TestForwardedHeadersAreIgnoredUnlessWeAreToldThereIsAProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.RemoteAddr = "10.0.0.2:5000"

	if overTLS(req, false) {
		t.Error("a plain request was treated as TLS on the strength of a header anyone can set")
	}
	if got := clientIP(req, false); got != "10.0.0.2" {
		t.Errorf("client ip = %q; a forwarded header was trusted without a proxy", got)
	}

	if !overTLS(req, true) {
		t.Error("behind a proxy, X-Forwarded-Proto: https was not honoured")
	}
	if got := clientIP(req, true); got != "203.0.113.9" {
		t.Errorf("client ip behind a proxy = %q, want the forwarded address", got)
	}
}

func TestTheDesignPreviewIsOffUnlessAskedFor(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	closed := New(db, quiet, "test.example", Options{}).Handler()

	if resp := get(t, closed, "/dev/preview"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the design preview answered %d on an instance that did not ask for it", resp.StatusCode)
	}

	open := New(db, quiet, "test.example", Options{ShowPreview: true}).Handler()
	if resp := get(t, open, "/dev/preview"); resp.StatusCode != http.StatusOK {
		t.Errorf("the design preview answered %d when it was switched on", resp.StatusCode)
	}
}
