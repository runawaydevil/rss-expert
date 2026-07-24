package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/runawaydevil/rss-expert/internal/store"
)

func testQueue(t *testing.T) *Queue {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-jobs")
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

type deliver struct {
	Target string `json:"target"`
	Item   string `json:"item"`
}

func TestEnqueueThenClaim(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	id, err := q.Enqueue(ctx, Spec{Kind: "deliver", Payload: deliver{"https://bob.example/hub", "p/1"}})
	if err != nil {
		t.Fatal(err)
	}

	job, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != id || job.Kind != "deliver" {
		t.Errorf("claimed %+v", job)
	}
	if job.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 after the first claim", job.Attempts)
	}

	var payload deliver
	if err := job.Into(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Target != "https://bob.example/hub" {
		t.Errorf("payload = %+v", payload)
	}

	if err := q.Done(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Errorf("a finished job was claimed again: %v", err)
	}
}

func TestAClaimedJobIsInvisibleToOtherWorkers(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	if _, err := q.Enqueue(ctx, Spec{Kind: "deliver", Payload: deliver{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Error("the same job was handed to two workers at once")
	}
}

func TestAnExpiredLeaseReturnsTheJob(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	if _, err := q.Enqueue(ctx, Spec{Kind: "deliver", Payload: deliver{}}); err != nil {
		t.Fatal(err)
	}
	first, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := q.db.Write.ExecContext(ctx,
		`update job set lease_until = ? where id = ?`,
		time.Now().Add(-time.Minute).Unix(), first.ID); err != nil {
		t.Fatal(err)
	}

	again, err := q.Claim(ctx)
	if err != nil {
		t.Fatalf("a job whose worker died was never returned to the queue: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("claimed job %d, want the abandoned %d", again.ID, first.ID)
	}
	if again.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", again.Attempts)
	}
}

func TestIdempotencyKeyBlocksADuplicate(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	spec := Spec{Kind: "deliver", Payload: deliver{}, IdemKey: "deliver:p/1:bob"}
	if _, err := q.Enqueue(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ctx, spec); !errors.Is(err, ErrDuplicate) {
		t.Errorf("the same job was queued twice: %v", err)
	}

	job, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Done(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := q.Enqueue(ctx, spec); err != nil {
		t.Errorf("the key stayed blocked after the job finished: %v", err)
	}
}

func TestFailureRetriesThenGivesUp(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	if _, err := q.Enqueue(ctx, Spec{Kind: "deliver", Payload: deliver{}, MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}

	job, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Fail(ctx, job, errors.New("the hub said no")); err != nil {
		t.Fatal(err)
	}

	depth, err := q.Depth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if depth.DeadLetter != 0 {
		t.Error("the job died on its first failure")
	}

	if _, err := q.db.Write.ExecContext(ctx, `update job set run_after = 0 where id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !job.LastChance() {
		t.Errorf("attempts = %d of %d; this should be the last try", job.Attempts, job.MaxAttempts)
	}
	if err := q.Fail(ctx, job, errors.New("the hub said no again")); err != nil {
		t.Fatal(err)
	}

	depth, err = q.Depth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if depth.DeadLetter != 1 {
		t.Errorf("dead letter holds %d jobs, want 1", depth.DeadLetter)
	}
	if _, err := q.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Error("a dead job was claimed again")
	}

	dead, err := q.DeadLetter(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 || dead[0].LastError != "the hub said no again" {
		t.Errorf("dead letter = %+v", dead)
	}

	if err := q.Retry(ctx, dead[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx); err != nil {
		t.Errorf("a retried job was not claimable: %v", err)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if Backoff(1) != BaseBackoff {
		t.Errorf("first backoff = %v, want %v", Backoff(1), BaseBackoff)
	}
	if Backoff(2) <= Backoff(1) {
		t.Error("backoff does not grow")
	}
	if Backoff(50) != MaxBackoff {
		t.Errorf("backoff at attempt 50 = %v, want the %v ceiling", Backoff(50), MaxBackoff)
	}
}

func TestRunAfterHoldsAJobBack(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	if _, err := q.Enqueue(ctx, Spec{
		Kind: "deliver", Payload: deliver{}, RunAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Error("a job scheduled for later was claimed now")
	}
}

func TestClaimCanFilterByKind(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	if _, err := q.Enqueue(ctx, Spec{Kind: "deliver", Payload: deliver{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ctx, Spec{Kind: "fetch", Payload: deliver{}}); err != nil {
		t.Fatal(err)
	}

	job, err := q.Claim(ctx, "fetch")
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != "fetch" {
		t.Errorf("claimed a %q job when only %q was asked for", job.Kind, "fetch")
	}
}

func TestConcurrentWorkersNeverShareAJob(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()

	const total = 60
	for i := 0; i < total; i++ {
		if _, err := q.Enqueue(ctx, Spec{Kind: "deliver", Payload: deliver{Item: string(rune('a' + i%26))}}); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu      sync.Mutex
		claimed = map[int64]int{}
		wg      sync.WaitGroup
	)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := q.Claim(ctx)
				if errors.Is(err, ErrNoJob) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				claimed[job.ID]++
				mu.Unlock()
				q.Done(ctx, job.ID)
			}
		}()
	}
	wg.Wait()

	if len(claimed) != total {
		t.Errorf("%d of %d jobs were processed", len(claimed), total)
	}
	for id, times := range claimed {
		if times != 1 {
			t.Errorf("job %d was handed out %d times", id, times)
		}
	}
}
