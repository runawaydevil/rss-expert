package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
)

func signedInAs(t *testing.T, h http.Handler, accounts *identity.Store, email string, role identity.Role) *http.Cookie {
	t.Helper()
	if _, err := accounts.Create(context.Background(), email, testPassword, role); err != nil {
		t.Fatal(err)
	}
	return signIn(t, h, email)
}

func getAs(t *testing.T, h http.Handler, path string, session *http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if session != nil {
		req.AddCookie(session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestAdminPanelIsClosedToTheWrongPeople(t *testing.T) {
	h, accounts := testAppWithAccounts(t)

	if resp := getAs(t, h, "/admin", nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a signed-out visitor got %d for /admin, want a redirect to sign in", resp.StatusCode)
	}

	reader := signedInAs(t, h, accounts, "reader@example.org", identity.RoleUser)
	if resp := getAs(t, h, "/admin", reader); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a plain user got %d for /admin, want 403", resp.StatusCode)
	}
}

func TestModeratorsReachThePanel(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	moderator := signedInAs(t, h, accounts, "mod@example.org", identity.RoleModerator)

	resp := getAs(t, h, "/admin", moderator)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a moderator got %d for /admin", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	for _, want := range []string{"Administration", "Queue", "Reports", "Audit log", "Dead letter"} {
		if !strings.Contains(page, want) {
			t.Errorf("the panel is missing %q", want)
		}
	}
}

func TestDestructiveActionsDemandAFreshLogin(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	owner := signedInAs(t, h, accounts, "owner@example.org", identity.RoleOwner)

	token, cookies := loginForm(t, h)
	resp := postForm(t, h, "/admin/job", url.Values{
		"csrf": {token}, "job": {"1"},
	}, append(cookies, owner))

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); !strings.HasPrefix(location, "/admin/confirm") {
		t.Errorf("a session that never reauthenticated went straight through to %q", location)
	}
}

func TestReauthenticationUnlocksThenTheActionRuns(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	owner := signedInAs(t, h, accounts, "owner@example.org", identity.RoleOwner)

	token, cookies := loginForm(t, h)
	all := append(cookies, owner)

	wrong := postForm(t, h, "/admin/confirm", url.Values{
		"csrf": {token}, "next": {"/admin"}, "password": {"not the password"},
	}, all)
	body, _ := io.ReadAll(wrong.Body)
	if !strings.Contains(string(body), "password is not right") {
		t.Error("a wrong password was accepted at the confirmation step")
	}

	right := postForm(t, h, "/admin/confirm", url.Values{
		"csrf": {token}, "next": {"/admin"}, "password": {testPassword},
	}, all)
	if right.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirming with the right password gave %d", right.StatusCode)
	}

	after := postForm(t, h, "/admin/job", url.Values{"csrf": {token}, "job": {"1"}}, all)
	if location := after.Header.Get("Location"); strings.HasPrefix(location, "/admin/confirm") {
		t.Error("the action still demanded confirmation after a successful one")
	}
}

func TestConfirmationRedirectStaysOnThisSite(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	owner := signedInAs(t, h, accounts, "owner@example.org", identity.RoleOwner)

	token, cookies := loginForm(t, h)
	all := append(cookies, owner)

	for _, hostile := range []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"javascript:alert(1)",
	} {
		resp := postForm(t, h, "/admin/confirm", url.Values{
			"csrf": {token}, "next": {hostile}, "password": {testPassword},
		}, all)
		if location := resp.Header.Get("Location"); location != "/admin" {
			t.Errorf("next=%q redirected to %q; it must stay on this site", hostile, location)
		}
	}
}

func TestTwoFactorEnrolmentPage(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	owner := signedInAs(t, h, accounts, "owner@example.org", identity.RoleOwner)

	resp := getAs(t, h, "/settings/two-factor", owner)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if !strings.Contains(page, "otpauth://totp/") {
		t.Error("the page offers no enrolment link")
	}
	if !strings.Contains(page, "required") {
		t.Error("an owner is not told that two-factor is required for their role")
	}
}

func TestAdministratorsCannotSwitchTwoFactorOff(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	owner := signedInAs(t, h, accounts, "owner@example.org", identity.RoleOwner)

	token, cookies := loginForm(t, h)
	resp := postForm(t, h, "/settings/two-factor/off", url.Values{"csrf": {token}}, append(cookies, owner))

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("an owner switched two-factor off and got %d", resp.StatusCode)
	}
}

func TestAPlainUserCanSwitchTheirOwnTwoFactorOff(t *testing.T) {
	h, accounts := testAppWithAccounts(t)
	reader := signedInAs(t, h, accounts, "reader@example.org", identity.RoleUser)

	token, cookies := loginForm(t, h)
	resp := postForm(t, h, "/settings/two-factor/off", url.Values{"csrf": {token}}, append(cookies, reader))

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a plain user got %d turning their own two-factor off", resp.StatusCode)
	}
}

func TestReturnAddressesCannotLeaveThisInstance(t *testing.T) {
	for _, hostile := range []string{
		"https://evil.example/", "//evil.example/", `/\evil.example`,
		"http://evil.example", "javascript:alert(1)", "/admin\r\nSet-Cookie: a=b",
	} {
		if got := safeNext(hostile); got != "/admin" {
			t.Errorf("safeNext(%q) = %q; it should have fallen back to /admin", hostile, got)
		}
	}
	if got := safeNext("/admin/confirm"); got != "/admin/confirm" {
		t.Errorf("a legitimate return address was thrown away: %q", got)
	}
}
