package push

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	HubLease    = 7 * 24 * time.Hour
	MaxCallback = 2000
)

var ErrNoSubscriber = errors.New("push: no such subscriber")

type Subscriber struct {
	ID         int64
	Topic      string
	Callback   string
	Secret     string
	Mode       string
	Challenge  string
	LeaseUntil time.Time
	Verified   bool
}

func (s *Store) Pending(ctx context.Context, topic, callback, secret, mode, challenge string) (int64, error) {
	now := time.Now().UTC()

	res, err := s.db.Write.ExecContext(ctx,
		`insert into hub_subscriber (topic, callback, secret, lease_until, mode, challenge, created_at)
		 values (?, ?, ?, ?, ?, ?, ?)
		 on conflict (topic, callback) do update set
		   secret = excluded.secret, mode = excluded.mode,
		   challenge = excluded.challenge, lease_until = excluded.lease_until`,
		topic, callback, nullable(secret), now.Add(HubLease).Unix(), mode, challenge, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("push: record subscriber: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		err = s.db.Read.QueryRowContext(ctx,
			`select id from hub_subscriber where topic = ? and callback = ?`, topic, callback).Scan(&id)
	}
	return id, err
}

func (s *Store) Subscriber(ctx context.Context, id int64) (*Subscriber, error) {
	var (
		sub                 Subscriber
		secret, challenge   sql.NullString
		verified, leaseUnix sql.NullInt64
	)
	err := s.db.Read.QueryRowContext(ctx,
		`select id, topic, callback, secret, mode, challenge, lease_until, verified_at
		 from hub_subscriber where id = ?`, id).
		Scan(&sub.ID, &sub.Topic, &sub.Callback, &secret, &sub.Mode, &challenge, &leaseUnix, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSubscriber
	}
	if err != nil {
		return nil, err
	}

	sub.Secret = secret.String
	sub.Challenge = challenge.String
	sub.Verified = verified.Valid
	sub.LeaseUntil = time.Unix(leaseUnix.Int64, 0).UTC()
	return &sub, nil
}

func (s *Store) Verified(ctx context.Context, id int64) error {
	_, err := s.db.Write.ExecContext(ctx,
		`update hub_subscriber set verified_at = ?, challenge = null where id = ?`,
		time.Now().UTC().Unix(), id)
	return err
}

func (s *Store) DropSubscriber(ctx context.Context, id int64) error {
	_, err := s.db.Write.ExecContext(ctx, `delete from hub_subscriber where id = ?`, id)
	return err
}

func (s *Store) Subscribers(ctx context.Context, topic string, now time.Time) ([]Subscriber, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select id, topic, callback, coalesce(secret, ''), lease_until
		 from hub_subscriber
		 where topic = ? and verified_at is not null and lease_until > ? and mode = 'subscribe'`,
		topic, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Subscriber
	for rows.Next() {
		var (
			sub   Subscriber
			lease int64
		)
		if err := rows.Scan(&sub.ID, &sub.Topic, &sub.Callback, &sub.Secret, &lease); err != nil {
			return nil, err
		}
		sub.LeaseUntil = time.Unix(lease, 0).UTC()
		sub.Verified = true
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) RegisterCloud(ctx context.Context, topic, callback string) error {
	now := time.Now().UTC()
	_, err := s.db.Write.ExecContext(ctx,
		`insert into cloud_subscriber (topic, callback, lease_until, created_at)
		 values (?, ?, ?, ?)
		 on conflict (topic, callback) do update set lease_until = excluded.lease_until`,
		topic, callback, now.Add(CloudLease).Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("push: register cloud subscriber: %w", err)
	}
	return nil
}

func (s *Store) CloudSubscribers(ctx context.Context, topic string, now time.Time) ([]string, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select callback from cloud_subscriber where topic = ? and lease_until > ?`, topic, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var callback string
		if err := rows.Scan(&callback); err != nil {
			return nil, err
		}
		out = append(out, callback)
	}
	return out, rows.Err()
}

func (s *Store) ForgetExpired(ctx context.Context, now time.Time) (int64, error) {
	hubs, err := s.db.Write.ExecContext(ctx,
		`delete from hub_subscriber where lease_until < ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	clouds, err := s.db.Write.ExecContext(ctx,
		`delete from cloud_subscriber where lease_until < ?`, now.Unix())
	if err != nil {
		return 0, err
	}

	a, _ := hubs.RowsAffected()
	b, _ := clouds.RowsAffected()
	return a + b, nil
}
