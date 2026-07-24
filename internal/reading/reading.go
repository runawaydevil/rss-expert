package reading

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

var (
	ErrNoCollection   = errors.New("no such collection")
	ErrCollectionName = errors.New("a collection needs a name")
	ErrNameTaken      = errors.New("you already have a collection with that name")
)

type Store struct {
	db *store.DB
}

func New(db *store.DB) *Store { return &Store{db: db} }

func (s *Store) MarkRead(ctx context.Context, accountID int64, keys ...string) error {
	return s.touch(ctx, accountID, "read_at", time.Now().UTC().Unix(), keys...)
}

func (s *Store) MarkUnread(ctx context.Context, accountID int64, keys ...string) error {
	return s.touch(ctx, accountID, "read_at", nil, keys...)
}

func (s *Store) Save(ctx context.Context, accountID int64, keys ...string) error {
	return s.touch(ctx, accountID, "saved_at", time.Now().UTC().Unix(), keys...)
}

func (s *Store) Unsave(ctx context.Context, accountID int64, keys ...string) error {
	return s.touch(ctx, accountID, "saved_at", nil, keys...)
}

func (s *Store) touch(ctx context.Context, accountID int64, column string, value any, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		_, err := s.db.Write.ExecContext(ctx,
			`insert into read_state (account_id, item_key, `+column+`) values (?, ?, ?)
			 on conflict (account_id, item_key) do update set `+column+` = excluded.`+column,
			accountID, key, value)
		if err != nil {
			return fmt.Errorf("reading: mark %s: %w", column, err)
		}
	}
	return nil
}

func (s *Store) MarkAllRead(ctx context.Context, accountID int64, before time.Time) (int64, error) {
	res, err := s.db.Write.ExecContext(ctx,
		`insert into read_state (account_id, item_key, read_at)
		 select ?, l.item_key, ?
		 from logical_item l
		 where coalesce(l.published_at, l.converged_at) <= ?
		 on conflict (account_id, item_key) do update set read_at = excluded.read_at
		 where read_state.read_at is null`,
		accountID, time.Now().UTC().Unix(), before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type Flags struct {
	Read  bool
	Saved bool
}

func (s *Store) FlagsFor(ctx context.Context, accountID int64, keys []string) (map[string]Flags, error) {
	out := make(map[string]Flags, len(keys))
	if accountID == 0 || len(keys) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+1)
	args = append(args, accountID)
	for _, key := range keys {
		args = append(args, key)
	}

	rows, err := s.db.Read.QueryContext(ctx,
		`select item_key, read_at is not null, saved_at is not null
		 from read_state where account_id = ? and item_key in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key   string
			flags Flags
		)
		if err := rows.Scan(&key, &flags.Read, &flags.Saved); err != nil {
			return nil, err
		}
		out[key] = flags
	}
	return out, rows.Err()
}

func (s *Store) UnreadCount(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx,
		`select count(*) from logical_item l
		 where not exists (
		   select 1 from read_state r
		   where r.account_id = ? and r.item_key = l.item_key and r.read_at is not null
		 )`, accountID).Scan(&n)
	return n, err
}

func (s *Store) SavedCount(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx,
		`select count(*) from read_state where account_id = ? and saved_at is not null`,
		accountID).Scan(&n)
	return n, err
}

type Collection struct {
	ID      int64
	Name    string
	Sources int
}

func (s *Store) CreateCollection(ctx context.Context, accountID int64, name string) (*Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrCollectionName
	}

	res, err := s.db.Write.ExecContext(ctx,
		`insert into collection (account_id, name, created_at) values (?, ?, ?)`,
		accountID, name, time.Now().UTC().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("reading: create collection: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Collection{ID: id, Name: name}, nil
}

func (s *Store) Collections(ctx context.Context, accountID int64) ([]Collection, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`select c.id, c.name, (select count(*) from collection_source cs where cs.collection_id = c.id)
		 from collection c where c.account_id = ? order by c.name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Sources); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddToCollection(ctx context.Context, accountID, collectionID, sourceID int64) error {
	if err := s.ownsCollection(ctx, accountID, collectionID); err != nil {
		return err
	}
	_, err := s.db.Write.ExecContext(ctx,
		`insert into collection_source (collection_id, source_id) values (?, ?) on conflict do nothing`,
		collectionID, sourceID)
	return err
}

func (s *Store) RemoveFromCollection(ctx context.Context, accountID, collectionID, sourceID int64) error {
	if err := s.ownsCollection(ctx, accountID, collectionID); err != nil {
		return err
	}
	_, err := s.db.Write.ExecContext(ctx,
		`delete from collection_source where collection_id = ? and source_id = ?`,
		collectionID, sourceID)
	return err
}

func (s *Store) DeleteCollection(ctx context.Context, accountID, collectionID int64) error {
	if err := s.ownsCollection(ctx, accountID, collectionID); err != nil {
		return err
	}
	_, err := s.db.Write.ExecContext(ctx, `delete from collection where id = ?`, collectionID)
	return err
}

func (s *Store) ownsCollection(ctx context.Context, accountID, collectionID int64) error {
	var owner int64
	err := s.db.Read.QueryRowContext(ctx, `select account_id from collection where id = ?`, collectionID).Scan(&owner)
	if err != nil || owner != accountID {
		return ErrNoCollection
	}
	return nil
}

func (s *Store) CollectionSources(ctx context.Context, accountID, collectionID int64) ([]int64, error) {
	if err := s.ownsCollection(ctx, accountID, collectionID); err != nil {
		return nil, err
	}
	rows, err := s.db.Read.QueryContext(ctx,
		`select source_id from collection_source where collection_id = ? order by source_id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
