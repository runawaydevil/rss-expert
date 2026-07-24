package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

const (
	PurposeVerify  = "verify"
	PurposeSignIn  = "signin"
	PurposeRecover = "recover"
	PurposeInvite  = "invite"
	tokenLifetime  = 2 * time.Hour
	inviteLifetime = 7 * 24 * time.Hour
)

var (
	ErrBadToken     = errors.New("identity: that link is not valid")
	ErrTokenExpired = errors.New("identity: that link has expired")
	ErrTokenUsed    = errors.New("identity: that link was already used")
)

type Token struct {
	Value     string
	AccountID int64
	Email     string
	Purpose   string
}

func (s *Store) IssueToken(ctx context.Context, accountID int64, email, purpose string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(value))

	lifetime := tokenLifetime
	if purpose == PurposeInvite {
		lifetime = inviteLifetime
	}

	now := time.Now().UTC()
	_, err := s.db.Write.ExecContext(ctx,
		`insert into email_token (account_id, email, purpose, token_hash, expires_at, created_at)
		 values (?, ?, ?, ?, ?, ?)`,
		nullableID(accountID), email, purpose, sum[:], now.Add(lifetime).Unix(), now.Unix())
	if err != nil {
		return "", err
	}
	return value, nil
}

func (s *Store) RedeemToken(ctx context.Context, value, purpose string) (*Token, error) {
	sum := sha256.Sum256([]byte(value))

	var (
		token     Token
		id        int64
		accountID sql.NullInt64
		expires   int64
		used      sql.NullInt64
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select id, account_id, email, expires_at, used_at from email_token
		 where token_hash = ? and purpose = ?`, sum[:], purpose).
		Scan(&id, &accountID, &token.Email, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadToken
	}
	if err != nil {
		return nil, err
	}
	if used.Valid {
		return nil, ErrTokenUsed
	}
	if time.Now().UTC().Unix() > expires {
		return nil, ErrTokenExpired
	}

	res, err := s.db.Write.ExecContext(ctx,
		`update email_token set used_at = ? where id = ? and used_at is null`,
		time.Now().UTC().Unix(), id)
	if err != nil {
		return nil, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil, ErrTokenUsed
	}

	token.Value = value
	token.AccountID = accountID.Int64
	token.Purpose = purpose
	return &token, nil
}

func (s *Store) MarkEmailVerified(ctx context.Context, accountID int64) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update account set email_verified_at = ? where id = ?`,
		time.Now().UTC().Unix(), accountID)
	return err
}

func (s *Store) EmailVerified(ctx context.Context, accountID int64) (bool, error) {
	var verified sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`select email_verified_at from account where id = ?`, accountID).Scan(&verified)
	return verified.Valid, err
}

func (s *Store) PurgeExpiredTokens(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.Write.ExecContext(ctx,
		`delete from email_token where expires_at < ? or used_at is not null`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (s *Store) PeekToken(ctx context.Context, value, purpose string) (*Token, error) {
	sum := sha256.Sum256([]byte(value))

	var (
		token     Token
		accountID sql.NullInt64
		expires   int64
		used      sql.NullInt64
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select account_id, email, expires_at, used_at from email_token
		 where token_hash = ? and purpose = ?`, sum[:], purpose).
		Scan(&accountID, &token.Email, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadToken
	}
	if err != nil {
		return nil, err
	}
	if used.Valid {
		return nil, ErrTokenUsed
	}
	if time.Now().UTC().Unix() > expires {
		return nil, ErrTokenExpired
	}
	token.AccountID = accountID.Int64
	token.Purpose = purpose
	return &token, nil
}
