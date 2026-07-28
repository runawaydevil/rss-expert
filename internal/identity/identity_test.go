package identity

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-identity")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=47104,t=1,p=1$") {
		t.Errorf("hash = %q", hash)
	}

	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Errorf("correct password rejected: ok=%v err=%v", ok, err)
	}

	ok, err = VerifyPassword(hash, "correct horse battery stapl")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("wrong password accepted")
	}
}

func TestPasswordsAreSalted(t *testing.T) {
	first, _ := HashPassword("the same password twice")
	second, _ := HashPassword("the same password twice")
	if first == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("got %v, want ErrPasswordTooShort", err)
	}
}

func TestMalformedHashIsAnError(t *testing.T) {
	for _, bad := range []string{
		"", "not a hash", "$argon2id$v=19$m=1$salt$hash",
		"$bcrypt$v=19$m=47104,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=99$m=47104,t=1,p=1$c2FsdA$aGFzaA",
	} {
		if _, err := VerifyPassword(bad, "whatever"); err == nil {
			t.Errorf("VerifyPassword(%q) returned no error", bad)
		}
	}
}

func TestCreateAndAuthenticate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	account, err := s.Create(ctx, "  Owner@Example.ORG ", "a long enough password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if account.Email != "Owner@Example.ORG" {
		t.Errorf("stored email = %q, want the address trimmed but not lowercased", account.Email)
	}

	got, err := s.Authenticate(ctx, "owner@example.org", "a long enough password")
	if err != nil {
		t.Fatalf("login with a differently cased address failed: %v", err)
	}
	if got.ID != account.ID || got.Role != RoleOwner {
		t.Errorf("authenticated as %+v", got)
	}

	if _, err := s.Authenticate(ctx, "owner@example.org", "the wrong password"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("got %v, want ErrBadCredentials", err)
	}
	if _, err := s.Authenticate(ctx, "nobody@example.org", "any password at all"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown address gave %v, want the same ErrBadCredentials as a wrong password", err)
	}
}

func TestOnlyOneOwner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "first@example.org", "a long enough password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "second@example.org", "a long enough password", RoleOwner); !errors.Is(err, ErrOwnerExists) {
		t.Errorf("got %v, want ErrOwnerExists", err)
	}
	if _, err := s.Create(ctx, "second@example.org", "a long enough password", RoleAdmin); err != nil {
		t.Errorf("a second admin should be allowed: %v", err)
	}
}

func TestDuplicateEmailRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "someone@example.org", "a long enough password", RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "SOMEONE@example.org", "a long enough password", RoleUser); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("got %v, want ErrEmailTaken for the same address in different case", err)
	}
}

func TestUnusableEmailRejected(t *testing.T) {
	s := testStore(t)
	for _, bad := range []string{"", "nobody", "@example.org", "someone@", "a b@example.org"} {
		if _, err := s.Create(context.Background(), bad, "a long enough password", RoleUser); !errors.Is(err, ErrEmailUnusable) {
			t.Errorf("Create(%q) gave %v, want ErrEmailUnusable", bad, err)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	account, err := s.Create(ctx, "reader@example.org", "a long enough password", RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	token, expires, err := s.CreateSession(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 40 {
		t.Errorf("session token is only %d characters", len(token))
	}
	if d := time.Until(expires); d < SessionLifetime-time.Minute {
		t.Errorf("session expires in %v, want about %v", d, SessionLifetime)
	}

	got, err := s.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != account.ID {
		t.Errorf("session resolved to account %d, want %d", got.ID, account.ID)
	}

	if err := s.DestroySession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("destroyed session gave %v, want ErrNoSession", err)
	}
}

func TestAdminSessionsAreShorter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	owner, err := s.Create(ctx, "owner@example.org", "a long enough password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	_, expires, err := s.CreateSession(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expires); d > AdminSessionLifetime+time.Minute {
		t.Errorf("owner session lasts %v, want at most %v", d, AdminSessionLifetime)
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	s := testStore(t)
	for _, token := range []string{"", "not-a-token", strings.Repeat("A", 43)} {
		if _, err := s.Session(context.Background(), token); !errors.Is(err, ErrNoSession) {
			t.Errorf("Session(%q) gave %v, want ErrNoSession", token, err)
		}
	}
}

func TestChangingPasswordEndsEverySession(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	account, err := s.Create(ctx, "reader@example.org", "a long enough password", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := s.CreateSession(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateSession(ctx, account)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetPassword(ctx, account.ID, "an entirely different password"); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"first": first, "second": second} {
		if _, err := s.Session(ctx, token); !errors.Is(err, ErrNoSession) {
			t.Errorf("%s session survived a password change", name)
		}
	}
	if _, err := s.Authenticate(ctx, "reader@example.org", "an entirely different password"); err != nil {
		t.Errorf("new password does not work: %v", err)
	}
}

func TestFailedInvitedRegistrationDoesNotConsumeTheInvitation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "member@example.org", "a long enough password", RoleUser); err != nil {
		t.Fatal(err)
	}
	invite, err := s.IssueToken(ctx, 0, "member@example.org", PurposeInvite)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateWithInvite(ctx, "member@example.org",
		"another long password", RoleUser, invite); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("registration returned %v, want ErrEmailTaken", err)
	}
	if _, err := s.PeekToken(ctx, invite, PurposeInvite); err != nil {
		t.Fatalf("the rolled-back registration consumed its invitation: %v", err)
	}
}

func TestFailedRecoveryDoesNotConsumeTheToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	token, err := s.IssueToken(ctx, 0, "missing@example.org", PurposeRecover)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverPassword(ctx, token, "a long enough password"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("recovery returned %v, want ErrNoAccount", err)
	}
	if _, err := s.PeekToken(ctx, token, PurposeRecover); err != nil {
		t.Fatalf("the rolled-back recovery consumed its token: %v", err)
	}
}

func TestBootstrapCreatesOwnerOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	result, err := Bootstrap(ctx, s, "owner@example.org", "a long enough password", quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if result != BootstrapCreated {
		t.Fatalf("first bootstrap returned %v, want BootstrapCreated", result)
	}

	owner, err := s.Owner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Email != "owner@example.org" || owner.Role != RoleOwner {
		t.Errorf("owner = %+v", owner)
	}

	result, err = Bootstrap(ctx, s, "someone-else@example.org", "another long password", quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if result != BootstrapOwnerAlreadyExists {
		t.Errorf("second bootstrap returned %v, want BootstrapOwnerAlreadyExists", result)
	}

	owner, _ = s.Owner(ctx)
	if owner.Email != "owner@example.org" {
		t.Error("a second bootstrap replaced the owner; it must never do that")
	}
	if _, err := s.Authenticate(ctx, "owner@example.org", "a long enough password"); err != nil {
		t.Errorf("the original owner password stopped working: %v", err)
	}
}

func TestBootstrapWithoutCredentialsDoesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	result, err := Bootstrap(ctx, s, "", "", quietLogger())
	if err != nil || result != BootstrapSkipped {
		t.Errorf("empty bootstrap gave %v, %v", result, err)
	}
	if n, _ := s.Count(ctx); n != 0 {
		t.Errorf("%d accounts were created from nothing", n)
	}

	if _, err := Bootstrap(ctx, s, "owner@example.org", "", quietLogger()); err == nil {
		t.Error("an email without a password was accepted")
	}
}

func TestPurgeExpiredSessions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	account, err := s.Create(ctx, "reader@example.org", "a long enough password", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(ctx, account)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.db.Write.ExecContext(ctx,
		`update session set expires_at = ? where account_id = ?`,
		time.Now().Add(-time.Hour).Unix(), account.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Session(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Error("an expired session still resolved")
	}

	n, err := s.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("purge removed %d rows, but reading the expired session should already have deleted it", n)
	}
}
