package identity

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTOTPMatchesTheRFC6238Vectors(t *testing.T) {
	secret := base32NoPad.EncodeToString([]byte("12345678901234567890"))

	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		got, err := TOTPCode(secret, time.Unix(tc.unix, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("at %d: code = %s, want %s (RFC 6238 test vector)", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyAcceptsTheNeighbouringStep(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	for _, drift := range []time.Duration{-TOTPStep, 0, TOTPStep} {
		code, err := TOTPCode(secret, now.Add(drift))
		if err != nil {
			t.Fatal(err)
		}
		if !VerifyTOTP(secret, code, now) {
			t.Errorf("a code from %v away was rejected; clocks are never exact", drift)
		}
	}

	stale, _ := TOTPCode(secret, now.Add(-5*TOTPStep))
	if VerifyTOTP(secret, stale, now) {
		t.Error("a code from two and a half minutes ago was accepted")
	}
}

func TestVerifyRejectsRubbish(t *testing.T) {
	secret, _ := NewTOTPSecret()
	for _, code := range []string{"", "12345", "1234567", "abcdef", "000000 ", "  "} {
		if VerifyTOTP(secret, code, time.Now()) {
			t.Errorf("VerifyTOTP accepted %q", code)
		}
	}
}

func TestVerifyIgnoresSpacing(t *testing.T) {
	secret, _ := NewTOTPSecret()
	code, _ := TOTPCode(secret, time.Now())
	spaced := code[:3] + " " + code[3:]
	if !VerifyTOTP(secret, spaced, time.Now()) {
		t.Errorf("%q was rejected; authenticator apps display codes with a space", spaced)
	}
}

func TestSecretIsUsableBase32(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(secret, "=") {
		t.Error("the secret is padded; many authenticator apps reject padding")
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatalf("the secret does not decode: %v", err)
	}
	if len(raw) != secretBytes {
		t.Errorf("secret is %d bytes, want %d", len(raw), secretBytes)
	}
}

func TestURICarriesWhatAnAppNeeds(t *testing.T) {
	uri := TOTPURI("rss expert", "owner@example.org", "ABCDEFGH")
	for _, want := range []string{
		"otpauth://totp/", "secret=ABCDEFGH", "issuer=rss+expert", "digits=6", "period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("the uri is missing %q: %s", want, uri)
		}
	}
}

func TestEnrolmentThenSignIn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	owner, err := s.Create(ctx, "owner@example.org", "a long enough password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	if on, _ := s.TOTPEnabled(ctx, owner.ID); on {
		t.Fatal("a fresh account already claims two-factor")
	}
	if err := s.CheckSecondFactor(ctx, owner, "123456"); !errors.Is(err, ErrTOTPNotSet) {
		t.Errorf("checking a factor that was never set gave %v", err)
	}

	secret, err := s.BeginTOTP(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if on, _ := s.TOTPEnabled(ctx, owner.ID); on {
		t.Error("two-factor counted as on before the first code was confirmed")
	}

	if _, err := s.ConfirmTOTP(ctx, owner, "000000"); !errors.Is(err, ErrTOTPBadCode) {
		t.Errorf("a wrong code confirmed enrolment: %v", err)
	}

	code, _ := TOTPCode(secret, time.Now())
	codes, err := s.ConfirmTOTP(ctx, owner, code)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodes {
		t.Errorf("got %d recovery codes, want %d", len(codes), RecoveryCodes)
	}

	if on, _ := s.TOTPEnabled(ctx, owner.ID); !on {
		t.Error("two-factor is not on after confirmation")
	}

	if err := s.CheckSecondFactor(ctx, owner, code); !errors.Is(err, ErrTOTPReused) {
		t.Errorf("the code that confirmed enrolment was accepted again at sign-in: %v", err)
	}

	live, _ := TOTPCode(secret, time.Now().Add(TOTPStep))
	if err := s.CheckSecondFactor(ctx, owner, live); err != nil {
		t.Errorf("a live code was rejected at sign-in: %v", err)
	}
	if err := s.CheckSecondFactor(ctx, owner, live); !errors.Is(err, ErrTOTPReused) {
		t.Error("the same code was accepted twice; a shoulder-surfer could sign in with it")
	}
	if err := s.CheckSecondFactor(ctx, owner, ""); !errors.Is(err, ErrTOTPRequired) {
		t.Errorf("an empty code passed: %v", err)
	}
}

func TestARecoveryCodeWorksOnceAndOnlyOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	owner, err := s.Create(ctx, "owner@example.org", "a long enough password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := s.BeginTOTP(ctx, owner)
	code, _ := TOTPCode(secret, time.Now())
	recovery, err := s.ConfirmTOTP(ctx, owner, code)
	if err != nil {
		t.Fatal(err)
	}

	left, _ := s.RecoveryCodesLeft(ctx, owner.ID)
	if left != RecoveryCodes {
		t.Fatalf("%d recovery codes left before any were used", left)
	}

	if err := s.CheckSecondFactor(ctx, owner, recovery[0]); err != nil {
		t.Fatalf("a recovery code was rejected: %v", err)
	}
	if err := s.CheckSecondFactor(ctx, owner, recovery[0]); !errors.Is(err, ErrTOTPBadCode) {
		t.Error("the same recovery code worked twice")
	}

	left, _ = s.RecoveryCodesLeft(ctx, owner.ID)
	if left != RecoveryCodes-1 {
		t.Errorf("%d codes left after using one", left)
	}

	if err := s.CheckSecondFactor(ctx, owner, strings.ToUpper(recovery[1])); err != nil {
		t.Errorf("a recovery code in capitals was rejected: %v", err)
	}
	if err := s.CheckSecondFactor(ctx, owner, strings.ReplaceAll(recovery[2], "-", "")); err != nil {
		t.Errorf("a recovery code without its dash was rejected: %v", err)
	}
}

func TestTurningTwoFactorOffClearsEverything(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	owner, _ := s.Create(ctx, "owner@example.org", "a long enough password", RoleOwner)
	secret, _ := s.BeginTOTP(ctx, owner)
	code, _ := TOTPCode(secret, time.Now())
	recovery, err := s.ConfirmTOTP(ctx, owner, code)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DisableTOTP(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.TOTPEnabled(ctx, owner.ID); on {
		t.Error("two-factor is still on")
	}
	if left, _ := s.RecoveryCodesLeft(ctx, owner.ID); left != 0 {
		t.Errorf("%d recovery codes survived", left)
	}
	if err := s.CheckSecondFactor(ctx, owner, recovery[0]); !errors.Is(err, ErrTOTPNotSet) {
		t.Errorf("an old recovery code still resolves: %v", err)
	}
}

func TestReauthenticationExpires(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	owner, _ := s.Create(ctx, "owner@example.org", "a long enough password", RoleOwner)
	token, _, err := s.CreateSession(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}

	fresh, err := s.ReauthenticatedWithin(ctx, token, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Error("a brand new session counts as recently reauthenticated")
	}

	if err := s.MarkReauthenticated(ctx, token); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.ReauthenticatedWithin(ctx, token, time.Minute); !fresh {
		t.Error("reauthentication did not take")
	}
	if fresh, _ := s.ReauthenticatedWithin(ctx, token, -time.Second); fresh {
		t.Error("reauthentication never expires")
	}
}
