package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const (
	SessionLifetime      = 30 * 24 * time.Hour
	AdminSessionLifetime = 12 * time.Hour
	sessionTokenBytes    = 32
)

var ErrNoSession = errors.New("no such session")

func (s *Store) CreateSession(ctx context.Context, account *Account) (string, time.Time, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	lifetime := SessionLifetime
	if account.Role.CanAdminister() {
		lifetime = AdminSessionLifetime
	}

	now := time.Now().UTC()
	expires := now.Add(lifetime)

	_, err := s.db.Write.ExecContext(ctx,
		`insert into session (token_hash, account_id, created_at, expires_at, last_seen)
		 values (?, ?, ?, ?, ?)`,
		hashToken(token), account.ID, now.Unix(), expires.Unix(), now.Unix())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("identity: create session: %w", err)
	}
	return token, expires, nil
}

func (s *Store) Session(ctx context.Context, token string) (*Account, error) {
	if token == "" {
		return nil, ErrNoSession
	}

	var accountID, expires int64
	err := s.db.Read.QueryRowContext(ctx,
		`select account_id, expires_at from session where token_hash = ?`, hashToken(token)).
		Scan(&accountID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("identity: look up session: %w", err)
	}

	if time.Now().UTC().Unix() >= expires {
		s.DestroySession(ctx, token)
		return nil, ErrNoSession
	}

	account, err := s.ByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account.Disabled() {
		s.destroyAllSessions(ctx, account.ID)
		return nil, ErrAccountDisabled
	}

	s.db.Write.ExecContext(ctx,
		`update session set last_seen = ? where token_hash = ?`,
		time.Now().UTC().Unix(), hashToken(token))

	return account, nil
}

func (s *Store) DestroySession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.Write.ExecContext(ctx, `delete from session where token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) destroyAllSessions(ctx context.Context, accountID int64) error {
	_, err := s.db.Write.ExecContext(ctx, `delete from session where account_id = ?`, accountID)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.Write.ExecContext(ctx,
		`delete from session where expires_at <= ?`, time.Now().UTC().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Store) MarkReauthenticated(ctx context.Context, token string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update session set reauth_at = ? where token_hash = ?`,
		time.Now().UTC().Unix(), hashToken(token))
	return err
}

func (s *Store) ReauthenticatedWithin(ctx context.Context, token string, window time.Duration) (bool, error) {
	var reauth sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`select reauth_at from session where token_hash = ?`, hashToken(token)).Scan(&reauth)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNoSession
	}
	if err != nil || !reauth.Valid {
		return false, err
	}
	return time.Since(time.Unix(reauth.Int64, 0)) <= window, nil
}
