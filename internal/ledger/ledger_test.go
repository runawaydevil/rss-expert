package ledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

func testLedger(t *testing.T) *Ledger {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-ledger")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func TestTheLedgerOnlyEverAppends(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()

	item := "https://alice.test/p/1"
	target := "https://bob.test/hub"

	for i, attempt := range []Attempt{
		{ItemKey: item, Target: target, Protocol: WebSub, AttemptNo: 1,
			Outcome: Failed, HTTPStatus: 503, Latency: 800 * time.Millisecond,
			ErrorKind: "http", ErrorDetail: "hub unavailable"},
		{ItemKey: item, Target: target, Protocol: WebSub, AttemptNo: 2,
			Outcome: OK, HTTPStatus: 204, Latency: 140 * time.Millisecond},
	} {
		attempt.At = time.Now().UTC().Add(time.Duration(i) * time.Second)
		if _, err := l.Record(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}

	journey, err := l.Journey(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if len(journey) != 2 {
		t.Fatalf("the journey holds %d attempts, want both", len(journey))
	}
	if journey[0].Outcome != Failed || journey[1].Outcome != OK {
		t.Errorf("the failure was overwritten instead of kept: %+v", journey)
	}
	if journey[0].ErrorDetail != "hub unavailable" {
		t.Errorf("the reason for the failure was lost: %q", journey[0].ErrorDetail)
	}
	if journey[1].Latency != 140*time.Millisecond {
		t.Errorf("latency = %v", journey[1].Latency)
	}
}

func TestCurrentStateIsTheLastRowPerTarget(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	item := "https://alice.test/p/1"

	attempts := []Attempt{
		{ItemKey: item, Target: "https://bob.test/hub", Protocol: WebSub, AttemptNo: 1, Outcome: Failed},
		{ItemKey: item, Target: "https://bob.test/hub", Protocol: WebSub, AttemptNo: 2, Outcome: OK, HTTPStatus: 204},
		{ItemKey: item, Target: "https://carol.test/webmention", Protocol: Webmention, AttemptNo: 1, Outcome: Pending},
	}
	for i, a := range attempts {
		a.At = time.Now().UTC().Add(time.Duration(i) * time.Second)
		if _, err := l.Record(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	states, err := l.Current(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("current state lists %d targets, want 2", len(states))
	}

	byTarget := map[string]State{}
	for _, s := range states {
		byTarget[s.Target] = s
	}
	if got := byTarget["https://bob.test/hub"]; got.Outcome != OK || got.AttemptNo != 2 {
		t.Errorf("the hub still reads as %+v; the retry that succeeded should win", got)
	}
	if got := byTarget["https://carol.test/webmention"]; got.Outcome != Pending {
		t.Errorf("the webmention reads as %+v", got)
	}
}

func TestFailingListsOnlyTheUnhappyOnes(t *testing.T) {
	l := testLedger(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, a := range []Attempt{
		{ItemKey: "a", Target: "t1", Protocol: WebSub, AttemptNo: 1, Outcome: OK, At: now},
		{ItemKey: "b", Target: "t2", Protocol: WebSub, AttemptNo: 1, Outcome: Failed, At: now},
		{ItemKey: "c", Target: "t3", Protocol: Webmention, AttemptNo: 3, Outcome: GaveUp, At: now},
		{ItemKey: "d", Target: "t4", Protocol: RSSCloud, AttemptNo: 1, Outcome: Pending, At: now},
	} {
		if _, err := l.Record(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	failing, err := l.Failing(ctx, now.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(failing) != 2 {
		t.Fatalf("failing lists %d attempts, want the failed and the gave-up", len(failing))
	}
	for _, a := range failing {
		if a.Outcome == OK || a.Outcome == Pending {
			t.Errorf("%s turned up in the failing list", a.Outcome)
		}
	}
}

func TestOutcomeSettlement(t *testing.T) {
	for outcome, settled := range map[Outcome]bool{
		Pending: false, Failed: false,
		OK: true, Rejected: true, GaveUp: true,
	} {
		if outcome.Settled() != settled {
			t.Errorf("%s.Settled() = %v, want %v", outcome, outcome.Settled(), settled)
		}
	}
}
