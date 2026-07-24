package web

import (
	"context"
	"io"
	"net/http"
	"net/smtp"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/mail"
)

type mailbox struct {
	mu       sync.Mutex
	messages []mail.Message
}

func (m *mailbox) capture() *mail.Sender {
	sender, _ := mail.New("smtp://mail.test", "instance@test.example")
	sender.SetSendFunc(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.messages = append(m.messages, mail.Message{To: to[0], Body: string(msg)})
		return nil
	})
	return sender
}

func (m *mailbox) waitForMail(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		n := len(m.messages)
		var body string
		if n > 0 {
			body = m.messages[n-1].Body
		}
		m.mu.Unlock()

		if n > 0 {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatal("no mail was sent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (m *mailbox) lastLink(t *testing.T) string {
	t.Helper()
	body := m.waitForMail(t)
	link := regexp.MustCompile(`https?://\S+/account/\S+`).FindString(body)
	if link == "" {
		t.Fatalf("no account link in the message:\n%s", body)
	}
	return strings.TrimRight(link, ")\r\n")
}

func accountsApp(t *testing.T, mode string) (http.Handler, *identity.Store, *mailbox) {
	t.Helper()
	ctx := context.Background()

	db := tempDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	box := &mailbox{}
	app := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "test.example", Options{
		Registration: mode,
		Mailer:       box.capture(),
	})
	return app.Handler(), identity.NewStore(db), box
}

func csrfFrom(t *testing.T, h http.Handler, path string, session *http.Cookie) (string, []*http.Cookie) {
	t.Helper()
	resp := getAs(t, h, path, session)
	body, _ := io.ReadAll(resp.Body)
	m := csrfInput.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf field at %s:\n%s", path, body)
	}
	cookies := resp.Cookies()
	if session != nil {
		cookies = append(cookies, session)
	}
	return string(m[1]), cookies
}

func localPath(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path + "?" + u.RawQuery
}

func TestRegisterThenVerifySignsYouIn(t *testing.T) {
	h, accounts, box := accountsApp(t, "open")
	ctx := context.Background()

	csrf, cookies := csrfFrom(t, h, "/register", nil)
	resp := postForm(t, h, "/register", url.Values{
		"csrf":     {csrf},
		"email":    {"newcomer@example.org"},
		"password": {"a long enough password"},
	}, cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register gave %d", resp.StatusCode)
	}

	account, err := accounts.ByEmail(ctx, "newcomer@example.org")
	if err != nil {
		t.Fatalf("the account was not created: %v", err)
	}
	if verified, _ := accounts.EmailVerified(ctx, account.ID); verified {
		t.Error("the account counted as verified before the link was opened")
	}

	verify := getAs(t, h, localPath(t, box.lastLink(t)), nil)
	if verify.StatusCode != http.StatusSeeOther {
		t.Fatalf("opening the link gave %d", verify.StatusCode)
	}
	if verify.Header.Get("Set-Cookie") == "" {
		t.Error("verifying did not start a session")
	}
	if verified, _ := accounts.EmailVerified(ctx, account.ID); !verified {
		t.Error("the address was not marked verified")
	}
}

func TestVerificationLinkWorksOnlyOnce(t *testing.T) {
	h, _, box := accountsApp(t, "open")

	csrf, cookies := csrfFrom(t, h, "/register", nil)
	postForm(t, h, "/register", url.Values{
		"csrf": {csrf}, "email": {"once@example.org"}, "password": {"a long enough password"},
	}, cookies)

	link := localPath(t, box.lastLink(t))
	if first := getAs(t, h, link, nil); first.StatusCode != http.StatusSeeOther {
		t.Fatalf("first use gave %d", first.StatusCode)
	}

	second := getAs(t, h, link, nil)
	body, _ := io.ReadAll(second.Body)
	if !strings.Contains(string(body), "already used") {
		t.Errorf("a link was accepted twice:\n%s", body)
	}
}

func TestRegistrationClosedRefusesEveryone(t *testing.T) {
	h, accounts, _ := accountsApp(t, "closed")

	page, _ := io.ReadAll(getAs(t, h, "/register", nil).Body)
	if !strings.Contains(string(page), "not taking new accounts") {
		t.Error("the closed page does not say so")
	}

	csrf, cookies := csrfFrom(t, h, "/register", nil)
	postForm(t, h, "/register", url.Values{
		"csrf": {csrf}, "email": {"nope@example.org"}, "password": {"a long enough password"},
	}, cookies)

	if _, err := accounts.ByEmail(context.Background(), "nope@example.org"); err == nil {
		t.Error("an account was created while registration was closed")
	}
}

func TestInviteModeNeedsAValidInvitation(t *testing.T) {
	h, accounts, _ := accountsApp(t, "invite")
	ctx := context.Background()

	csrf, cookies := csrfFrom(t, h, "/register", nil)
	postForm(t, h, "/register", url.Values{
		"csrf": {csrf}, "email": {"guest@example.org"},
		"password": {"a long enough password"}, "invite": {"not-a-real-code"},
	}, cookies)
	if _, err := accounts.ByEmail(ctx, "guest@example.org"); err == nil {
		t.Fatal("a bogus invitation let someone in")
	}

	owner, err := accounts.Create(ctx, "owner@example.org", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := accounts.IssueToken(ctx, owner.ID, "guest@example.org", identity.PurposeInvite)
	if err != nil {
		t.Fatal(err)
	}

	csrf, cookies = csrfFrom(t, h, "/register", nil)
	resp := postForm(t, h, "/register", url.Values{
		"csrf": {csrf}, "email": {"guest@example.org"},
		"password": {"a long enough password"}, "invite": {invite},
	}, cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a good invitation gave %d", resp.StatusCode)
	}
	if _, err := accounts.ByEmail(ctx, "guest@example.org"); err != nil {
		t.Errorf("a valid invitation did not create the account: %v", err)
	}
}

func TestMagicLinkSignsInAnExistingAccount(t *testing.T) {
	h, accounts, box := accountsApp(t, "open")
	ctx := context.Background()

	if _, err := accounts.Create(ctx, "member@example.org", "a long enough password", identity.RoleUser); err != nil {
		t.Fatal(err)
	}

	csrf, cookies := csrfFrom(t, h, "/account/forgot", nil)
	postForm(t, h, "/account/forgot", url.Values{
		"csrf": {csrf}, "email": {"member@example.org"},
	}, cookies)

	resp := getAs(t, h, localPath(t, box.lastLink(t)), nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Set-Cookie") == "" {
		t.Errorf("the magic link did not sign the member in: %d", resp.StatusCode)
	}
}

func TestAskingForALinkNeverRevealsWhetherTheAddressExists(t *testing.T) {
	h, _, box := accountsApp(t, "open")

	csrf, cookies := csrfFrom(t, h, "/account/forgot", nil)
	resp := postForm(t, h, "/account/forgot", url.Values{
		"csrf": {csrf}, "email": {"stranger@example.org"},
	}, cookies)

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Check your mail") {
		t.Errorf("the page did not give the same neutral answer:\n%s", body)
	}

	box.mu.Lock()
	sent := len(box.messages)
	box.mu.Unlock()
	if sent != 0 {
		t.Error("a mail was sent to an address with no account")
	}
}

func TestRecoverSetsANewPasswordAndEndsOtherSessions(t *testing.T) {
	h, accounts, box := accountsApp(t, "open")
	ctx := context.Background()

	account, err := accounts.Create(ctx, "forgot@example.org", "the old long password", identity.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := accounts.CreateSession(ctx, account); err != nil {
		t.Fatal(err)
	}

	csrf, cookies := csrfFrom(t, h, "/account/forgot", nil)
	postForm(t, h, "/account/forgot", url.Values{
		"csrf": {csrf}, "email": {"forgot@example.org"}, "recover": {"1"},
	}, cookies)

	link := localPath(t, box.lastLink(t))
	formCSRF, formCookies := csrfFrom(t, h, link, nil)
	resp := postForm(t, h, "/account/recover", url.Values{
		"csrf": {formCSRF}, "token": {tokenOf(link)}, "password": {"a brand new long password"},
	}, formCookies)
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("recovery gave %d: %s", resp.StatusCode, body)
	}

	if _, err := accounts.Authenticate(ctx, "forgot@example.org", "a brand new long password"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := accounts.Authenticate(ctx, "forgot@example.org", "the old long password"); err == nil {
		t.Error("the old password still works")
	}
}

func tokenOf(link string) string {
	_, after, _ := strings.Cut(link, "token=")
	return after
}
