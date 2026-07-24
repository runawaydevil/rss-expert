package moderation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T) (*Store, *identity.Store) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-mod")
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
	return New(db), identity.NewStore(db)
}

func people(t *testing.T, accounts *identity.Store) (owner, moderator, reader *identity.Account) {
	t.Helper()
	ctx := context.Background()

	var err error
	if owner, err = accounts.Create(ctx, "owner@example.org", "a long enough password", identity.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if moderator, err = accounts.Create(ctx, "mod@example.org", "a long enough password", identity.RoleModerator); err != nil {
		t.Fatal(err)
	}
	if reader, err = accounts.Create(ctx, "reader@example.org", "a long enough password", identity.RoleUser); err != nil {
		t.Fatal(err)
	}
	return
}

func TestAReaderCanOnlyBlockForThemselves(t *testing.T) {
	s, accounts := testStore(t)
	_, _, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, reader, reader.ID, Domain, "https://noisy.example/feed.xml", "too much"); err != nil {
		t.Fatalf("a reader could not block for themselves: %v", err)
	}
	if err := s.Block(ctx, reader, 0, Domain, "noisy.example", ""); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a reader blocked something instance-wide: %v", err)
	}
}

func TestModeratorsBlockForTheWholeInstance(t *testing.T) {
	s, accounts := testStore(t)
	_, moderator, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, moderator, 0, Domain, "spam.example", "a spam farm"); err != nil {
		t.Fatal(err)
	}

	filter, err := s.FilterFor(ctx, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	hidden, why := filter.Hides("https://spam.example/p/1", "https://spam.example/p/1", "", "hello")
	if !hidden {
		t.Error("an instance-wide block did not reach a reader who never set it")
	}
	if why == "" {
		t.Error("the reader is given no reason the item is hidden")
	}
}

func TestDomainBlockIsNormalised(t *testing.T) {
	s, accounts := testStore(t)
	_, moderator, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, moderator, 0, Domain, "https://WWW.Spam.Example/feed.xml", ""); err != nil {
		t.Fatal(err)
	}

	filter, _ := s.FilterFor(ctx, reader.ID)
	for _, link := range []string{
		"https://spam.example/p/1",
		"http://www.spam.example/other",
		"https://SPAM.example/p/2",
	} {
		if hidden, _ := filter.Hides(link, link, "", ""); !hidden {
			t.Errorf("%s slipped past the domain block", link)
		}
	}
	if hidden, _ := filter.Hides("https://notspam.example/p/1", "https://notspam.example/p/1", "", ""); hidden {
		t.Error("an unrelated domain was blocked")
	}
}

func TestMutedWords(t *testing.T) {
	s, accounts := testStore(t)
	_, _, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, reader, reader.ID, Word, "Cryptocurrency", ""); err != nil {
		t.Fatal(err)
	}

	filter, _ := s.FilterFor(ctx, reader.ID)
	if hidden, _ := filter.Hides("k", "", "", "Everything about CRYPTOCURRENCY today"); !hidden {
		t.Error("a muted word did not match in a different case")
	}
	if hidden, _ := filter.Hides("k", "", "", "Everything about gardening"); hidden {
		t.Error("an unrelated post was muted")
	}
}

func TestOneReadersBlockDoesNotReachAnother(t *testing.T) {
	s, accounts := testStore(t)
	owner, _, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, reader, reader.ID, Domain, "quiet.example", ""); err != nil {
		t.Fatal(err)
	}

	theirs, _ := s.FilterFor(ctx, reader.ID)
	if hidden, _ := theirs.Hides("https://quiet.example/p/1", "https://quiet.example/p/1", "", ""); !hidden {
		t.Error("their own block does not apply to them")
	}

	others, _ := s.FilterFor(ctx, owner.ID)
	if hidden, _ := others.Hides("https://quiet.example/p/1", "https://quiet.example/p/1", "", ""); hidden {
		t.Error("one reader's private block leaked to another account")
	}
}

func TestUnblock(t *testing.T) {
	s, accounts := testStore(t)
	_, _, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, reader, reader.ID, Domain, "gone.example", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Unblock(ctx, reader, reader.ID, Domain, "gone.example"); err != nil {
		t.Fatal(err)
	}

	filter, _ := s.FilterFor(ctx, reader.ID)
	if hidden, _ := filter.Hides("https://gone.example/p/1", "https://gone.example/p/1", "", ""); hidden {
		t.Error("the block survived being removed")
	}
}

func TestReportsAreDecidedOnlyByModerators(t *testing.T) {
	s, accounts := testStore(t)
	_, moderator, reader := people(t, accounts)
	ctx := context.Background()

	id, err := s.Report(ctx, reader, "https://spam.example/p/1", "spam", "the whole feed is like this")
	if err != nil {
		t.Fatal(err)
	}

	open, err := s.OpenReports(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != id {
		t.Fatalf("open reports = %+v", open)
	}

	if err := s.Decide(ctx, reader, id, true, "agreed"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a plain reader decided a report: %v", err)
	}
	if err := s.Decide(ctx, moderator, id, true, "clear spam"); err != nil {
		t.Fatal(err)
	}

	open, _ = s.OpenReports(ctx, 10)
	if len(open) != 0 {
		t.Errorf("%d reports still open after a decision", len(open))
	}
	if err := s.Decide(ctx, moderator, id, false, ""); !errors.Is(err, ErrNoReport) {
		t.Errorf("a decided report was decided again: %v", err)
	}
}

func TestEveryModerationActionIsAudited(t *testing.T) {
	s, accounts := testStore(t)
	owner, moderator, reader := people(t, accounts)
	ctx := context.Background()

	if err := s.Block(ctx, moderator, 0, Domain, "spam.example", "spam"); err != nil {
		t.Fatal(err)
	}
	id, _ := s.Report(ctx, reader, "https://spam.example/p/1", "spam", "")
	if err := s.Decide(ctx, moderator, id, true, "upheld"); err != nil {
		t.Fatal(err)
	}
	if err := s.Block(ctx, owner, 0, Word, "slur", ""); err != nil {
		t.Fatal(err)
	}

	entries, err := s.Audit(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("the audit log holds %d entries, want 3", len(entries))
	}

	var sawOwner bool
	for _, e := range entries {
		if e.Actor == owner.ID {
			sawOwner = true
			if e.Role != string(identity.RoleOwner) {
				t.Errorf("the owner's action is logged with role %q", e.Role)
			}
		}
	}
	if !sawOwner {
		t.Error("the owner's own action was not audited; nobody should be exempt")
	}
}
