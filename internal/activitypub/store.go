package activitypub

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

const ActorTTL = 24 * time.Hour

var ErrNoFollower = errors.New("activitypub: nobody by that name follows this account")

type Store struct {
	db *store.DB
}

func New(db *store.DB) *Store { return &Store{db: db} }

func (s *Store) EnsureKey(ctx context.Context, accountID int64) (*rsa.PrivateKey, string, error) {
	private, public, err := s.readKey(ctx, accountID)
	if err == nil {
		key, err := ParsePrivateKey(private)
		return key, public, err
	}
	if !errors.Is(err, ErrNoKey) {
		return nil, "", err
	}

	pair, err := NewKeyPair()
	if err != nil {
		return nil, "", err
	}
	_, err = s.db.Write.ExecContext(ctx,
		`update account set ap_private_key = ?, ap_public_key = ?
		 where id = ? and ap_private_key is null`,
		pair.PrivatePEM, pair.PublicPEM, accountID)
	if err != nil {
		return nil, "", fmt.Errorf("activitypub: store key: %w", err)
	}

	private, public, err = s.readKey(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	key, err := ParsePrivateKey(private)
	return key, public, err
}

func (s *Store) readKey(ctx context.Context, accountID int64) (string, string, error) {
	var private, public sql.NullString
	err := s.db.Read.QueryRowContext(ctx,
		`select ap_private_key, ap_public_key from account where id = ?`, accountID).
		Scan(&private, &public)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNoKey
	}
	if err != nil {
		return "", "", err
	}
	if !private.Valid || private.String == "" {
		return "", "", ErrNoKey
	}
	return private.String, public.String, nil
}

func (s *Store) PublicKeyFor(ctx context.Context, accountID int64) (string, error) {
	_, public, err := s.EnsureKey(ctx, accountID)
	return public, err
}

func (s *Store) RememberActor(ctx context.Context, actor *Actor) error {
	host := hostOf(actor.ID)
	if host == "" {
		return ErrNotAnActor
	}

	_, err := s.db.Write.ExecContext(ctx,
		`insert into remote_actor
		   (actor, inbox, shared_inbox, public_key_id, public_key_pem, username, name, fetched_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (actor) do update set
		   inbox = excluded.inbox, shared_inbox = excluded.shared_inbox,
		   public_key_id = excluded.public_key_id, public_key_pem = excluded.public_key_pem,
		   username = excluded.username, name = excluded.name, fetched_at = excluded.fetched_at`,
		actor.ID, actor.Inbox, nullable(sharedInboxOf(actor)),
		actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM,
		nullable(actor.PreferredUsername), nullable(actor.Name),
		time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("activitypub: remember actor: %w", err)
	}
	return nil
}

func (s *Store) CachedActor(ctx context.Context, uri string) (*Actor, bool) {
	var (
		actor       Actor
		shared      sql.NullString
		username    sql.NullString
		name        sql.NullString
		key         PublicKey
		fetchedUnix int64
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select actor, inbox, shared_inbox, public_key_id, public_key_pem, username, name, fetched_at
		 from remote_actor where actor = ?`, uri).
		Scan(&actor.ID, &actor.Inbox, &shared, &key.ID, &key.PublicKeyPEM,
			&username, &name, &fetchedUnix)
	if err != nil {
		return nil, false
	}

	if time.Since(time.Unix(fetchedUnix, 0)) > ActorTTL {
		return nil, false
	}

	key.Owner = actor.ID
	actor.PublicKey = &key
	actor.PreferredUsername = username.String
	actor.Name = name.String
	if shared.Valid && shared.String != "" {
		actor.Endpoints = &Endpoints{SharedInbox: shared.String}
	}
	return &actor, true
}

func (s *Store) AddFollower(ctx context.Context, accountID int64, actor *Actor) error {
	_, err := s.db.Write.ExecContext(ctx,
		`insert into follower (account_id, actor, inbox, shared_inbox, created_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (account_id, actor) do update set
		   inbox = excluded.inbox, shared_inbox = excluded.shared_inbox`,
		accountID, actor.ID, actor.Inbox, nullable(sharedInboxOf(actor)),
		time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("activitypub: add follower: %w", err)
	}
	return nil
}

func (s *Store) RemoveFollower(ctx context.Context, accountID int64, actorURI string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`delete from follower where account_id = ? and actor = ?`, accountID, actorURI)
	return err
}

func (s *Store) Followers(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select distinct coalesce(nullif(shared_inbox, ''), inbox)
		 from follower where account_id = ?`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var inbox string
		if err := rows.Scan(&inbox); err != nil {
			return nil, err
		}
		out = append(out, inbox)
	}
	return out, rows.Err()
}

func (s *Store) CountFollowers(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx,
		`select count(*) from follower where account_id = ?`, accountID).Scan(&n)
	return n, err
}

func (s *Store) AlreadySeen(ctx context.Context, activityID string) bool {
	if activityID == "" {
		return false
	}

	res, err := s.db.Write.ExecContext(ctx,
		`insert into inbox_seen (activity_id, seen_at) values (?, ?)
		 on conflict (activity_id) do nothing`,
		activityID, time.Now().UTC().Unix())
	if err != nil {
		return false
	}
	inserted, _ := res.RowsAffected()
	return inserted == 0
}

func (s *Store) ForgetOldActivities(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.Write.ExecContext(ctx,
		`delete from inbox_seen where seen_at < ?`,
		time.Now().UTC().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func sharedInboxOf(actor *Actor) string {
	if actor.Endpoints == nil {
		return ""
	}
	return actor.Endpoints.SharedInbox
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) RecordReaction(ctx context.Context, itemKey, actor, kind, activityID string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`insert into reaction (item_key, actor, kind, activity_id, created_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (item_key, actor, kind) do update set
		   activity_id = excluded.activity_id`,
		itemKey, actor, kind, activityID, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("activitypub: record reaction: %w", err)
	}
	return nil
}

func (s *Store) ForgetReaction(ctx context.Context, itemKey, actor, kind string) error {
	_, err := s.db.Write.ExecContext(ctx,
		`delete from reaction where item_key = ? and actor = ? and kind = ?`,
		itemKey, actor, kind)
	return err
}

type Reactions struct {
	Likes  int
	Boosts int
}

func (s *Store) ReactionsTo(ctx context.Context, itemKey string) (Reactions, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select kind, count(*) from reaction where item_key = ? group by kind`, itemKey)
	if err != nil {
		return Reactions{}, err
	}
	defer rows.Close()

	var counted Reactions
	for rows.Next() {
		var (
			kind string
			n    int
		)
		if err := rows.Scan(&kind, &n); err != nil {
			return Reactions{}, err
		}
		switch kind {
		case "Like":
			counted.Likes = n
		case "Announce":
			counted.Boosts = n
		}
	}
	return counted, rows.Err()
}

func (s *Store) SigningFor(ctx context.Context, inbox string) string {
	host := hostOf(inbox)
	if host == "" {
		return SigningCavage
	}

	var scheme string
	err := s.db.Read.QueryRowContext(ctx,
		`select signing from remote_host where host = ?`, host).Scan(&scheme)
	if err != nil || (scheme != SigningCavage && scheme != Signing9421) {
		return SigningCavage
	}
	return scheme
}

func (s *Store) RememberSigning(ctx context.Context, inbox, scheme string) {
	host := hostOf(inbox)
	if host == "" {
		return
	}
	_, _ = s.db.Write.ExecContext(ctx,
		`insert into remote_host (host, signing, updated_at) values (?, ?, ?)
		 on conflict (host) do update set signing = excluded.signing, updated_at = excluded.updated_at`,
		host, scheme, time.Now().UTC().Unix())
}
