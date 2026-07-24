package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

const (
	DefaultMaxAttempts = 5
	DefaultLease       = 5 * time.Minute
	BaseBackoff        = 30 * time.Second
	MaxBackoff         = 6 * time.Hour
)

var (
	ErrNoJob     = errors.New("no such job")
	ErrDuplicate = errors.New("a job with that idempotency key is already waiting")
)

type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
	IdemKey     string
	LastError   string
	RunAfter    time.Time
	CreatedAt   time.Time
}

func (j *Job) Into(target any) error {
	return json.Unmarshal(j.Payload, target)
}

func (j *Job) LastChance() bool { return j.Attempts >= j.MaxAttempts }

type Queue struct {
	db    *store.DB
	lease time.Duration
}

func New(db *store.DB) *Queue {
	return &Queue{db: db, lease: DefaultLease}
}

type Spec struct {
	Kind        string
	Payload     any
	IdemKey     string
	RunAfter    time.Time
	MaxAttempts int
}

func (q *Queue) Enqueue(ctx context.Context, spec Spec) (int64, error) {
	if spec.Kind == "" {
		return 0, errors.New("jobs: a job needs a kind")
	}
	if spec.MaxAttempts <= 0 {
		spec.MaxAttempts = DefaultMaxAttempts
	}

	payload, err := json.Marshal(spec.Payload)
	if err != nil {
		return 0, fmt.Errorf("jobs: encode payload: %w", err)
	}

	now := time.Now().UTC()
	runAfter := spec.RunAfter
	if runAfter.IsZero() {
		runAfter = now
	}

	res, err := q.db.Write.ExecContext(ctx,
		`insert into job (kind, payload, run_after, max_attempts, idem_key, created_at)
		 values (?, ?, ?, ?, ?, ?)`,
		spec.Kind, string(payload), runAfter.Unix(), spec.MaxAttempts,
		nullable(spec.IdemKey), now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrDuplicate
		}
		return 0, fmt.Errorf("jobs: enqueue: %w", err)
	}
	return res.LastInsertId()
}

func (q *Queue) Claim(ctx context.Context, kinds ...string) (*Job, error) {
	now := time.Now().UTC()

	filter := ""
	args := []any{now.Add(q.lease).Unix(), now.Unix(), now.Unix()}
	if len(kinds) > 0 {
		filter = " and kind in (" + placeholders(len(kinds)) + ")"
		for _, kind := range kinds {
			args = append(args, kind)
		}
	}

	row := q.db.Write.QueryRowContext(ctx,
		`update job set lease_until = ?, attempts = attempts + 1
		 where id = (
		   select id from job
		   where done_at is null and dead_at is null
		     and run_after <= ?
		     and (lease_until is null or lease_until <= ?)`+filter+`
		   order by run_after limit 1
		 )
		 returning id, kind, payload, attempts, max_attempts,
		           coalesce(idem_key, ''), coalesce(last_error, ''), run_after, created_at`,
		args...)

	var (
		job               Job
		payload           string
		runAfter, created int64
	)
	err := row.Scan(&job.ID, &job.Kind, &payload, &job.Attempts, &job.MaxAttempts,
		&job.IdemKey, &job.LastError, &runAfter, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoJob
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: claim: %w", err)
	}

	job.Payload = json.RawMessage(payload)
	job.RunAfter = time.Unix(runAfter, 0).UTC()
	job.CreatedAt = time.Unix(created, 0).UTC()
	return &job, nil
}

func (q *Queue) Done(ctx context.Context, id int64) error {
	_, err := q.db.Write.ExecContext(ctx,
		`update job set done_at = ?, lease_until = null where id = ?`,
		time.Now().UTC().Unix(), id)
	return err
}

func (q *Queue) Fail(ctx context.Context, job *Job, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}

	if job.LastChance() {
		_, err := q.db.Write.ExecContext(ctx,
			`update job set dead_at = ?, last_error = ?, lease_until = null where id = ?`,
			time.Now().UTC().Unix(), nullable(message), job.ID)
		return err
	}

	retryAt := time.Now().UTC().Add(Backoff(job.Attempts))
	_, err := q.db.Write.ExecContext(ctx,
		`update job set run_after = ?, last_error = ?, lease_until = null where id = ?`,
		retryAt.Unix(), nullable(message), job.ID)
	return err
}

func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	grown := float64(BaseBackoff) * math.Pow(2, float64(attempt-1))
	if grown > float64(MaxBackoff) {
		return MaxBackoff
	}
	return time.Duration(grown)
}

func (q *Queue) Retry(ctx context.Context, id int64) error {
	res, err := q.db.Write.ExecContext(ctx,
		`update job set dead_at = null, attempts = 0, run_after = ?, lease_until = null
		 where id = ? and dead_at is not null`,
		time.Now().UTC().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoJob
	}
	return nil
}

type Depth struct {
	Ready      int
	Leased     int
	DeadLetter int
	Done       int
}

func (q *Queue) Depth(ctx context.Context) (Depth, error) {
	var d Depth
	now := time.Now().UTC().Unix()
	err := q.db.Read.QueryRowContext(ctx,
		`select
		  (select count(*) from job where done_at is null and dead_at is null and run_after <= ? and (lease_until is null or lease_until <= ?)),
		  (select count(*) from job where done_at is null and dead_at is null and lease_until > ?),
		  (select count(*) from job where dead_at is not null),
		  (select count(*) from job where done_at is not null)`,
		now, now, now).Scan(&d.Ready, &d.Leased, &d.DeadLetter, &d.Done)
	return d, err
}

func (q *Queue) DeadLetter(ctx context.Context, limit int) ([]*Job, error) {
	rows, err := q.db.Read.QueryContext(ctx,
		`select id, kind, payload, attempts, max_attempts, coalesce(idem_key, ''),
		        coalesce(last_error, ''), run_after, created_at
		 from job where dead_at is not null order by dead_at desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Job
	for rows.Next() {
		var (
			job               Job
			payload           string
			runAfter, created int64
		)
		if err := rows.Scan(&job.ID, &job.Kind, &payload, &job.Attempts, &job.MaxAttempts,
			&job.IdemKey, &job.LastError, &runAfter, &created); err != nil {
			return nil, err
		}
		job.Payload = json.RawMessage(payload)
		job.RunAfter = time.Unix(runAfter, 0).UTC()
		job.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, &job)
	}
	return out, rows.Err()
}

func (q *Queue) PurgeDone(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := q.db.Write.ExecContext(ctx,
		`delete from job where done_at is not null and done_at < ?`,
		time.Now().UTC().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
