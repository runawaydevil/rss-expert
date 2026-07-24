package push

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

const (
	WebSub   = "websub"
	RSSCloud = "rsscloud"

	LeaseAsked   = 7 * 24 * time.Hour
	RenewAt      = 12 * time.Hour
	CloudLease   = 25 * time.Hour
	CloudRenewAt = 20 * time.Hour
)

var (
	ErrNoIntent    = errors.New("push: nobody asked for that subscription")
	ErrBadSecret   = errors.New("push: the signature does not match the secret we gave the hub")
	ErrUnsigned    = errors.New("push: a delivery arrived without a signature but we asked for one")
	ErrUnknownHash = errors.New("push: the hub signed with an algorithm we do not know")
)

type Store struct {
	db *store.DB
}

func New(db *store.DB) *Store { return &Store{db: db} }

type Subscription struct {
	SourceID int64
	Protocol string
	Topic    string
	Mode     string
	Secret   string
}

func (s *Store) Intend(ctx context.Context, sub Subscription) error {
	_, err := s.db.Write.ExecContext(ctx,
		`insert into push_intent (source_id, protocol, topic, mode, secret, created_at)
		 values (?, ?, ?, ?, ?, ?)
		 on conflict (source_id, protocol) do update set
		   topic = excluded.topic, mode = excluded.mode,
		   secret = excluded.secret, created_at = excluded.created_at`,
		sub.SourceID, sub.Protocol, sub.Topic, sub.Mode, nullable(sub.Secret), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("push: record intent: %w", err)
	}
	return nil
}

func (s *Store) Intent(ctx context.Context, sourceID int64, protocol string) (*Subscription, error) {
	var (
		sub    Subscription
		secret sql.NullString
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select source_id, protocol, topic, mode, secret from push_intent
		 where source_id = ? and protocol = ?`, sourceID, protocol).
		Scan(&sub.SourceID, &sub.Protocol, &sub.Topic, &sub.Mode, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoIntent
	}
	if err != nil {
		return nil, err
	}
	sub.Secret = secret.String
	return &sub, nil
}

func (s *Store) Confirm(ctx context.Context, sourceID int64, protocol string, lease time.Duration) error {
	sub, err := s.Intent(ctx, sourceID, protocol)
	if err != nil {
		return err
	}

	until := time.Now().UTC().Add(lease).Unix()
	if sub.Mode == "unsubscribe" {
		until = 0
	}

	switch protocol {
	case WebSub:
		_, err = s.db.Write.ExecContext(ctx,
			`update source set hub_lease_until = ?, hub_secret = ? where id = ?`,
			until, nullable(sub.Secret), sourceID)
	case RSSCloud:
		_, err = s.db.Write.ExecContext(ctx,
			`update source set cloud_until = ? where id = ?`, until, sourceID)
	}
	if err != nil {
		return err
	}

	_, err = s.db.Write.ExecContext(ctx,
		`delete from push_intent where source_id = ? and protocol = ?`, sourceID, protocol)
	return err
}

func (s *Store) Remember(ctx context.Context, sourceID int64, found Endpoints) error {
	var endpoint any
	if found.Cloud != nil {
		endpoint = found.Cloud.Endpoint()
	}

	_, err := s.db.Write.ExecContext(ctx,
		`update source set hub_url = ?, self_link = ?, cloud_endpoint = ? where id = ?`,
		nullable(found.Hub), nullable(found.Self), endpoint, sourceID)
	if err != nil {
		return fmt.Errorf("push: remember endpoints: %w", err)
	}
	return nil
}

type Due struct {
	SourceID int64
	FeedURL  string
	Topic    string
	Hub      string
	Cloud    string
	Protocol string
}

func (s *Store) DueForRenewal(ctx context.Context, now time.Time, limit int) ([]Due, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, feed_url, coalesce(self_link, feed_url), coalesce(hub_url, ''),
		        coalesce(cloud_endpoint, ''), coalesce(hub_lease_until, 0), coalesce(cloud_until, 0)
		 from source
		 where quarantined_at is null
		   and ((hub_url is not null and coalesce(hub_lease_until, 0) < ?)
		     or (cloud_endpoint is not null and coalesce(cloud_until, 0) < ?))
		 limit ?`,
		now.Add(RenewAt).Unix(), now.Add(CloudRenewAt).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Due
	for rows.Next() {
		var (
			d                      Due
			hubUntil, cloudUntil   int64
			hub, cloud, topic, url string
			id                     int64
		)
		if err := rows.Scan(&id, &url, &topic, &hub, &cloud, &hubUntil, &cloudUntil); err != nil {
			return nil, err
		}
		d = Due{SourceID: id, FeedURL: url, Topic: topic, Hub: hub, Cloud: cloud}

		if hub != "" && hubUntil < now.Add(RenewAt).Unix() {
			d.Protocol = WebSub
			out = append(out, d)
		}
		if cloud != "" && cloudUntil < now.Add(CloudRenewAt).Unix() {
			d.Protocol = RSSCloud
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

func (s *Store) SecretFor(ctx context.Context, sourceID int64) (string, error) {
	var secret sql.NullString
	err := s.db.Read.QueryRowContext(ctx,
		`select hub_secret from source where id = ?`, sourceID).Scan(&secret)
	return secret.String, err
}

func NewSecret() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func CheckSignature(header, secret string, body []byte) error {
	if secret == "" {
		return nil
	}
	if header == "" {
		return ErrUnsigned
	}

	method, sent, ok := strings.Cut(header, "=")
	if !ok {
		return ErrUnknownHash
	}

	var maker func() hash.Hash
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "sha1":
		maker = sha1.New
	case "sha256":
		maker = sha256.New
	case "sha384":
		maker = sha512.New384
	case "sha512":
		maker = sha512.New
	default:
		return ErrUnknownHash
	}

	mac := hmac.New(maker, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimSpace(sent))) != 1 {
		return ErrBadSecret
	}
	return nil
}

func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func SubscribeForm(callback, topic, secret, mode string) url.Values {
	form := url.Values{
		"hub.callback":      {callback},
		"hub.mode":          {mode},
		"hub.topic":         {topic},
		"hub.lease_seconds": {strconv.Itoa(int(LeaseAsked.Seconds()))},
	}
	if secret != "" && mode == "subscribe" {
		form.Set("hub.secret", secret)
	}
	return form
}

func CloudForm(callbackPath, callbackHost string, port int, topic string) url.Values {
	return url.Values{
		"notifyProcedure": {""},
		"protocol":        {"http-post"},
		"path":            {callbackPath},
		"port":            {strconv.Itoa(port)},
		"domain":          {callbackHost},
		"url1":            {topic},
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
