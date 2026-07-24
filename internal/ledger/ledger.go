package ledger

import (
	"context"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

type Outcome string

const (
	Pending  Outcome = "pending"
	OK       Outcome = "ok"
	Rejected Outcome = "rejected"
	Failed   Outcome = "failed"
	GaveUp   Outcome = "gaveup"
)

func (o Outcome) Settled() bool {
	return o == OK || o == Rejected || o == GaveUp
}

type Protocol string

const (
	WebSub     Protocol = "websub"
	RSSCloud   Protocol = "rsscloud"
	Webmention Protocol = "webmention"
)

type Attempt struct {
	ID          int64
	ItemKey     string
	Target      string
	Protocol    Protocol
	AttemptNo   int
	HTTPStatus  int
	Latency     time.Duration
	Outcome     Outcome
	ErrorKind   string
	ErrorDetail string
	At          time.Time
}

type Ledger struct {
	db *store.DB
}

func New(db *store.DB) *Ledger { return &Ledger{db: db} }

func (l *Ledger) Record(ctx context.Context, a Attempt) (int64, error) {
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	res, err := l.db.Write.ExecContext(ctx,
		`insert into delivery_attempt
		   (item_key, target, protocol, attempt_no, http_status, latency_ms,
		    outcome, error_kind, error_detail, at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ItemKey, a.Target, string(a.Protocol), a.AttemptNo,
		zeroToNil(a.HTTPStatus), zeroToNil(int(a.Latency.Milliseconds())),
		string(a.Outcome), nullable(a.ErrorKind), nullable(a.ErrorDetail), a.At.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const attemptColumns = `id, item_key, target, protocol, attempt_no,
	coalesce(http_status, 0), coalesce(latency_ms, 0), outcome,
	coalesce(error_kind, ''), coalesce(error_detail, ''), at`

func scan(row interface{ Scan(...any) error }) (Attempt, error) {
	var (
		a        Attempt
		protocol string
		outcome  string
		latency  int64
		at       int64
	)
	err := row.Scan(&a.ID, &a.ItemKey, &a.Target, &protocol, &a.AttemptNo,
		&a.HTTPStatus, &latency, &outcome, &a.ErrorKind, &a.ErrorDetail, &at)
	if err != nil {
		return a, err
	}
	a.Protocol = Protocol(protocol)
	a.Outcome = Outcome(outcome)
	a.Latency = time.Duration(latency) * time.Millisecond
	a.At = time.Unix(at, 0).UTC()
	return a, nil
}

func (l *Ledger) collect(ctx context.Context, query string, args ...any) ([]Attempt, error) {
	rows, err := l.db.Read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Attempt
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (l *Ledger) Journey(ctx context.Context, itemKey string) ([]Attempt, error) {
	return l.collect(ctx,
		`select `+attemptColumns+` from delivery_attempt where item_key = ? order by at, id`,
		itemKey)
}

func (l *Ledger) Failing(ctx context.Context, since time.Time, limit int) ([]Attempt, error) {
	return l.collect(ctx,
		`select `+attemptColumns+` from delivery_attempt
		 where at >= ? and outcome in ('failed', 'gaveup', 'rejected')
		 order by at desc limit ?`, since.Unix(), limit)
}

type State struct {
	Target    string
	Protocol  Protocol
	Outcome   Outcome
	AttemptNo int
	Latency   time.Duration
	At        time.Time
	Detail    string
}

func (l *Ledger) Current(ctx context.Context, itemKey string) ([]State, error) {
	rows, err := l.db.Read.QueryContext(ctx,
		`select target, protocol, outcome, attempt_no, coalesce(latency_ms, 0),
		        at, coalesce(error_detail, '')
		 from delivery_attempt d
		 where item_key = ?
		   and id = (select max(id) from delivery_attempt e
		             where e.item_key = d.item_key and e.target = d.target
		               and e.protocol = d.protocol)
		 order by target, protocol`, itemKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []State
	for rows.Next() {
		var (
			s                 State
			protocol, outcome string
			latency, at       int64
		)
		if err := rows.Scan(&s.Target, &protocol, &outcome, &s.AttemptNo, &latency, &at, &s.Detail); err != nil {
			return nil, err
		}
		s.Protocol = Protocol(protocol)
		s.Outcome = Outcome(outcome)
		s.Latency = time.Duration(latency) * time.Millisecond
		s.At = time.Unix(at, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func zeroToNil(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
