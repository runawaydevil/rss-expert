package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	TOTPStep       = 30 * time.Second
	TOTPDigits     = 6
	TOTPWindow     = 1
	RecoveryCodes  = 10
	secretBytes    = 20
	recoveryLength = 10
)

var (
	ErrTOTPNotSet    = errors.New("two-factor is not set up on this account")
	ErrTOTPBadCode   = errors.New("that code is not right")
	ErrTOTPRequired  = errors.New("this account requires a two-factor code")
	ErrTOTPAlreadyOn = errors.New("two-factor is already switched on")
	ErrTOTPReused    = errors.New("that code has already been used; wait for the next one")
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

func NewTOTPSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(raw), nil
}

func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := base32NoPad.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("identity: unreadable totp secret: %w", err)
	}

	counter := make([]byte, 8)
	binary.BigEndian.PutUint64(counter, uint64(at.UTC().Unix()/int64(TOTPStep.Seconds())))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter)
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	modulus := uint32(1)
	for i := 0; i < TOTPDigits; i++ {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", TOTPDigits, truncated%modulus), nil
}

func VerifyTOTP(secret, code string, at time.Time) bool {
	_, ok := matchTOTP(secret, code, at)
	return ok
}

func matchTOTP(secret, code string, at time.Time) (int64, bool) {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != TOTPDigits {
		return 0, false
	}

	for drift := -TOTPWindow; drift <= TOTPWindow; drift++ {
		moment := at.Add(time.Duration(drift) * TOTPStep)
		want, err := TOTPCode(secret, moment)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return moment.UTC().Unix() / int64(TOTPStep.Seconds()), true
		}
	}
	return 0, false
}

func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	query := url.Values{
		"secret":    {secret},
		"issuer":    {issuer},
		"digits":    {fmt.Sprint(TOTPDigits)},
		"period":    {fmt.Sprint(int(TOTPStep.Seconds()))},
		"algorithm": {"SHA1"},
	}
	return "otpauth://totp/" + label + "?" + query.Encode()
}

func (s *Store) BeginTOTP(ctx context.Context, account *Account) (secret string, err error) {
	on, err := s.TOTPEnabled(ctx, account.ID)
	if err != nil {
		return "", err
	}
	if on {
		return "", ErrTOTPAlreadyOn
	}

	secret, err = NewTOTPSecret()
	if err != nil {
		return "", err
	}
	_, err = s.db.Write.ExecContext(ctx,
		`update account set totp_secret = ?, totp_confirmed_at = null where id = ?`,
		secret, account.ID)
	return secret, err
}

func (s *Store) ConfirmTOTP(ctx context.Context, account *Account, code string) ([]string, error) {
	var secret sql.NullString
	err := s.db.Read.QueryRowContext(ctx, `select totp_secret from account where id = ?`, account.ID).Scan(&secret)
	if err != nil {
		return nil, err
	}
	if !secret.Valid || secret.String == "" {
		return nil, ErrTOTPNotSet
	}
	step, ok := matchTOTP(secret.String, code, time.Now())
	if !ok {
		return nil, ErrTOTPBadCode
	}

	if _, err := s.db.Write.ExecContext(ctx,
		`update account set totp_confirmed_at = ?, totp_last_step = ? where id = ?`,
		time.Now().UTC().Unix(), step, account.ID); err != nil {
		return nil, err
	}
	return s.regenerateRecoveryCodes(ctx, account.ID)
}

func (s *Store) DisableTOTP(ctx context.Context, account *Account) error {
	if _, err := s.db.Write.ExecContext(ctx,
		`update account set totp_secret = null, totp_confirmed_at = null where id = ?`,
		account.ID); err != nil {
		return err
	}
	_, err := s.db.Write.ExecContext(ctx, `delete from recovery_code where account_id = ?`, account.ID)
	return err
}

func (s *Store) TOTPEnabled(ctx context.Context, accountID int64) (bool, error) {
	var confirmed sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`select totp_confirmed_at from account where id = ?`, accountID).Scan(&confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNoAccount
	}
	return confirmed.Valid, err
}

func (s *Store) CheckSecondFactor(ctx context.Context, account *Account, code string) error {
	var secret sql.NullString
	var confirmed, lastStep sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`select totp_secret, totp_confirmed_at, totp_last_step from account where id = ?`, account.ID).
		Scan(&secret, &confirmed, &lastStep)
	if err != nil {
		return err
	}
	if !confirmed.Valid {
		return ErrTOTPNotSet
	}

	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if code == "" {
		return ErrTOTPRequired
	}

	if step, ok := matchTOTP(secret.String, code, time.Now()); ok {
		if lastStep.Valid && step <= lastStep.Int64 {
			return ErrTOTPReused
		}
		_, err := s.db.Write.ExecContext(ctx,
			`update account set totp_last_step = ? where id = ?`, step, account.ID)
		return err
	}
	return s.useRecoveryCode(ctx, account.ID, code)
}

func (s *Store) regenerateRecoveryCodes(ctx context.Context, accountID int64) ([]string, error) {
	if _, err := s.db.Write.ExecContext(ctx, `delete from recovery_code where account_id = ?`, accountID); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Unix()
	codes := make([]string, 0, RecoveryCodes)
	for i := 0; i < RecoveryCodes; i++ {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		hash := hashRecovery(code)
		if _, err := s.db.Write.ExecContext(ctx,
			`insert into recovery_code (account_id, code_hash, created_at) values (?, ?, ?)`,
			accountID, hash, now); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

func (s *Store) useRecoveryCode(ctx context.Context, accountID int64, code string) error {
	res, err := s.db.Write.ExecContext(ctx,
		`update recovery_code set used_at = ?
		 where account_id = ? and code_hash = ? and used_at is null`,
		time.Now().UTC().Unix(), accountID, hashRecovery(code))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTOTPBadCode
	}
	return nil
}

func (s *Store) RecoveryCodesLeft(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx,
		`select count(*) from recovery_code where account_id = ? and used_at is null`,
		accountID).Scan(&n)
	return n, err
}

func newRecoveryCode() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	raw := make([]byte, recoveryLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	var b strings.Builder
	for i, v := range raw {
		if i == recoveryLength/2 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

func hashRecovery(code string) []byte {
	normalised := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(normalised))
	return sum[:]
}
