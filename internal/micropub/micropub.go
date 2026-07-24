package micropub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

const (
	ScopeCreate = "create"
	ScopeUpdate = "update"
	ScopeDelete = "delete"
	ScopeMedia  = "media"
)

var (
	ErrNoToken      = errors.New("no access token was presented")
	ErrBadToken     = errors.New("that access token is not valid")
	ErrScopeMissing = errors.New("that token does not carry the scope this needs")
	ErrUnsupported  = errors.New("this endpoint does not support that request")
)

type Token struct {
	ID       int64
	Account  *identity.Account
	ClientID string
	Scope    string
}

func (t *Token) Allows(scope string) bool {
	for _, granted := range strings.Fields(t.Scope) {
		if granted == scope {
			return true
		}
	}
	return false
}

type Store struct {
	db       *store.DB
	accounts *identity.Store
}

func New(db *store.DB) *Store {
	return &Store{db: db, accounts: identity.NewStore(db)}
}

func (s *Store) Issue(ctx context.Context, account *identity.Account, clientID, scope string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	sum := sha256.Sum256([]byte(token))
	_, err := s.db.Write.ExecContext(ctx,
		`insert into token (account_id, token_hash, client_id, scope, created_at)
		 values (?, ?, ?, ?, ?)`,
		account.ID, sum[:], clientID, scope, time.Now().UTC().Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) Revoke(ctx context.Context, accountID, tokenID int64) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update token set revoked_at = ? where id = ? and account_id = ?`,
		time.Now().UTC().Unix(), tokenID, accountID)
	return err
}

func (s *Store) Resolve(ctx context.Context, raw string) (*Token, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrNoToken
	}

	sum := sha256.Sum256([]byte(raw))
	var (
		token     Token
		accountID int64
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select id, account_id, client_id, scope from token
		 where token_hash = ? and revoked_at is null`, sum[:]).
		Scan(&token.ID, &accountID, &token.ClientID, &token.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadToken
	}
	if err != nil {
		return nil, err
	}

	account, err := s.accounts.ByID(ctx, accountID)
	if err != nil {
		return nil, ErrBadToken
	}
	if account.Disabled() {
		return nil, ErrBadToken
	}
	token.Account = account

	s.db.Write.ExecContext(ctx, `update token set last_used = ? where id = ?`,
		time.Now().UTC().Unix(), token.ID)

	return &token, nil
}

func BearerToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		if fields := strings.Fields(header); len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			return fields[1]
		}
	}
	return r.FormValue("access_token")
}

type Request struct {
	Action    string
	URL       string
	Type      string
	Name      string
	Content   string
	InReplyTo string
	Media     []string
	Replace   map[string][]string
}

var mediaProperties = []string{"photo", "audio", "video"}

func ParseForm(values map[string][]string) (*Request, error) {
	req := &Request{
		Action:    first(values["action"]),
		URL:       first(values["url"]),
		Type:      first(values["h"]),
		Name:      first(values["name"]),
		Content:   first(values["content"]),
		InReplyTo: first(values["in-reply-to"]),
	}
	for _, key := range mediaProperties {
		req.Media = append(req.Media, values[key]...)
		req.Media = append(req.Media, values[key+"[]"]...)
	}
	if req.Action == "" {
		req.Action = "create"
	}
	if req.Type == "" {
		req.Type = "entry"
	}
	if req.Action == "create" && req.Type != "entry" {
		return nil, ErrUnsupported
	}
	return req, nil
}

type jsonRequest struct {
	Action     string           `json:"action"`
	URL        string           `json:"url"`
	Type       []string         `json:"type"`
	Properties map[string][]any `json:"properties"`
	Replace    map[string][]any `json:"replace"`
}

func ParseJSON(body []byte) (*Request, error) {
	var doc jsonRequest
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}

	req := &Request{Action: doc.Action, URL: doc.URL, Type: "entry"}
	if req.Action == "" {
		req.Action = "create"
	}
	if len(doc.Type) > 0 && doc.Type[0] != "h-entry" {
		return nil, ErrUnsupported
	}

	req.Name = firstString(doc.Properties["name"])
	req.Content = contentOf(doc.Properties["content"])
	req.InReplyTo = firstString(doc.Properties["in-reply-to"])

	for _, key := range mediaProperties {
		for _, value := range doc.Properties[key] {
			if nested, ok := value.(map[string]any); ok {
				value = nested["value"]
			}
			if s := stringOf(value); s != "" {
				req.Media = append(req.Media, s)
			}
		}
	}

	if len(doc.Replace) > 0 {
		req.Replace = map[string][]string{}
		for key, values := range doc.Replace {
			for _, value := range values {
				if s := stringOf(value); s != "" {
					req.Replace[key] = append(req.Replace[key], s)
				}
			}
		}
	}
	return req, nil
}

func contentOf(values []any) string {
	if len(values) == 0 {
		return ""
	}
	if nested, ok := values[0].(map[string]any); ok {
		for _, key := range []string{"markdown", "text", "html"} {
			if s := stringOf(nested[key]); s != "" {
				return s
			}
		}
		return ""
	}
	return stringOf(values[0])
}

func firstString(values []any) string {
	if len(values) == 0 {
		return ""
	}
	return stringOf(values[0])
}

func stringOf(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

type Issued struct {
	ID        int64
	ClientID  string
	Scope     string
	CreatedAt time.Time
	LastUsed  time.Time
}

func (s *Store) ForAccount(ctx context.Context, accountID int64) ([]Issued, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, client_id, scope, created_at, coalesce(last_used, 0) from token
		 where account_id = ? and revoked_at is null order by created_at desc`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Issued
	for rows.Next() {
		var (
			issued            Issued
			created, lastUsed int64
		)
		if err := rows.Scan(&issued.ID, &issued.ClientID, &issued.Scope, &created, &lastUsed); err != nil {
			return nil, err
		}
		issued.CreatedAt = time.Unix(created, 0).UTC()
		if lastUsed > 0 {
			issued.LastUsed = time.Unix(lastUsed, 0).UTC()
		}
		out = append(out, issued)
	}
	return out, rows.Err()
}
