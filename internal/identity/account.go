package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleModerator Role = "moderator"
	RoleUser      Role = "user"
)

func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleModerator, RoleUser:
		return true
	}
	return false
}

func (r Role) CanModerate() bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleModerator
}

func (r Role) CanAdminister() bool {
	return r == RoleOwner || r == RoleAdmin
}

var (
	ErrNoAccount        = errors.New("no such account")
	ErrEmailTaken       = errors.New("that email address is already registered")
	ErrOwnerExists      = errors.New("this instance already has an owner")
	ErrAccountDisabled  = errors.New("account is disabled")
	ErrBadCredentials   = errors.New("email or password is wrong")
	ErrEmailUnusable    = errors.New("that does not look like an email address")
	ErrRoleNotSupported = errors.New("unknown role")
)

type Account struct {
	ID         int64
	Email      string
	Role       Role
	CreatedAt  time.Time
	DisabledAt *time.Time
}

func (a *Account) Disabled() bool { return a != nil && a.DisabledAt != nil }

func (a *Account) Initial() string {
	for _, r := range a.Email {
		return strings.ToUpper(string(r))
	}
	return "?"
}

type Store struct {
	db *store.DB
}

func NewStore(db *store.DB) *Store { return &Store{db: db} }

func fold(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func usableEmail(email string) error {
	folded := fold(email)
	at := strings.IndexByte(folded, '@')
	if at <= 0 || at == len(folded)-1 || strings.Contains(folded, " ") {
		return ErrEmailUnusable
	}
	return nil
}

func (s *Store) Create(ctx context.Context, email, password string, role Role) (*Account, error) {
	if err := usableEmail(email); err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, ErrRoleNotSupported
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	return createAccount(ctx, s.db.Write, email, hash, role)
}

type accountExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func createAccount(ctx context.Context, db accountExecer, email, hash string, role Role) (*Account, error) {
	now := time.Now().UTC()
	res, err := db.ExecContext(ctx,
		`insert into account (email, email_folded, password_hash, role, created_at)
		 values (?, ?, ?, ?, ?)`,
		strings.TrimSpace(email), fold(email), hash, string(role), now.Unix())
	if err != nil {
		if violatesUnique(err, "account.role") {
			return nil, ErrOwnerExists
		}
		if violatesUnique(err, "account.email_folded") {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("identity: create account: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Account{ID: id, Email: strings.TrimSpace(email), Role: role, CreatedAt: now}, nil
}

func (s *Store) CreateWithInvite(ctx context.Context, email, password string, role Role, invite string) (*Account, error) {
	if err := usableEmail(email); err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, ErrRoleNotSupported
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	token, err := redeemTokenTx(ctx, tx, invite, PurposeInvite)
	if err != nil || !strings.EqualFold(token.Email, strings.TrimSpace(email)) {
		if err == nil {
			err = ErrBadToken
		}
		return nil, err
	}
	account, err := createAccount(ctx, tx, email, hash, role)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Store) Authenticate(ctx context.Context, email, password string) (*Account, error) {
	account, hash, err := s.byEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNoAccount) {
			decoyVerify(password)
			return nil, ErrBadCredentials
		}
		return nil, err
	}

	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBadCredentials
	}
	if account.Disabled() {
		return nil, ErrAccountDisabled
	}
	return account, nil
}

func (s *Store) byEmail(ctx context.Context, email string) (*Account, string, error) {
	var (
		a          Account
		hash       string
		created    int64
		disabledAt sql.NullInt64
		role       string
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select id, email, password_hash, role, created_at, disabled_at
		 from account where email_folded = ?`, fold(email)).
		Scan(&a.ID, &a.Email, &hash, &role, &created, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNoAccount
	}
	if err != nil {
		return nil, "", fmt.Errorf("identity: look up account: %w", err)
	}

	a.Role = Role(role)
	a.CreatedAt = time.Unix(created, 0).UTC()
	if disabledAt.Valid {
		t := time.Unix(disabledAt.Int64, 0).UTC()
		a.DisabledAt = &t
	}
	return &a, hash, nil
}

func (s *Store) ByID(ctx context.Context, id int64) (*Account, error) {
	var (
		a          Account
		created    int64
		disabledAt sql.NullInt64
		role       string
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select id, email, role, created_at, disabled_at from account where id = ?`, id).
		Scan(&a.ID, &a.Email, &role, &created, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, fmt.Errorf("identity: look up account: %w", err)
	}

	a.Role = Role(role)
	a.CreatedAt = time.Unix(created, 0).UTC()
	if disabledAt.Valid {
		t := time.Unix(disabledAt.Int64, 0).UTC()
		a.DisabledAt = &t
	}
	return &a, nil
}

func (s *Store) Owner(ctx context.Context) (*Account, error) {
	var id int64
	err := s.db.Read.QueryRowContext(ctx, `select id from account where role = 'owner'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, fmt.Errorf("identity: look up owner: %w", err)
	}
	return s.ByID(ctx, id)
}

func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx, `select count(*) from account`).Scan(&n)
	return n, err
}

func (s *Store) SetPassword(ctx context.Context, id int64, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.Write.ExecContext(ctx,
		`update account set password_hash = ? where id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("identity: set password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoAccount
	}
	return s.destroyAllSessions(ctx, id)
}

func (s *Store) RecoverPassword(ctx context.Context, token, password string) (int64, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	redeemed, err := redeemTokenTx(ctx, tx, token, PurposeRecover)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`update account set password_hash = ? where id = ?`, hash, redeemed.AccountID)
	if err != nil {
		return 0, fmt.Errorf("identity: set password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNoAccount
	}
	if _, err := tx.ExecContext(ctx,
		`delete from session where account_id = ?`, redeemed.AccountID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return redeemed.AccountID, nil
}

func violatesUnique(err error, column string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+column)
}

var decoyHash = sync.OnceValue(func() string {
	hash, err := hashWith(current, "no account has ever used this as a password")
	if err != nil {
		return ""
	}
	return hash
})

func decoyVerify(password string) {
	if hash := decoyHash(); hash != "" {
		VerifyPassword(hash, password)
	}
}

func (s *Store) ByEmail(ctx context.Context, email string) (*Account, error) {
	account, _, err := s.byEmail(ctx, email)
	return account, err
}
