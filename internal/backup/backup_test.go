package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/publish"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func liveInstance(t *testing.T) (*store.DB, string) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-backup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o750); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(ctx, filepath.Join(data, "rss-expert.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db, dir
}

func seed(t *testing.T, db *store.DB) (email, guid string) {
	t.Helper()
	ctx := context.Background()

	accounts := identity.NewStore(db)
	owner, err := accounts.Create(ctx, "owner@example.org", "a long enough password", identity.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	posts := publish.NewStore(db, "example.org")
	post, err := posts.Create(ctx, owner, "Kept", "This has to survive a restore.", "")
	if err != nil {
		t.Fatal(err)
	}
	return owner.Email, post.GUID
}

func TestBackupThenRestoreIntoAnEmptyDirectory(t *testing.T) {
	ctx := context.Background()

	db, root := liveInstance(t)
	email, guid := seed(t, db)

	into := filepath.Join(root, "backup")
	manifest, err := Take(ctx, db, into, "0.0.1-test")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion == 0 {
		t.Error("the manifest records no schema version")
	}
	if len(manifest.Files) != 1 || manifest.Files[0].SHA256 == "" {
		t.Fatalf("manifest files = %+v", manifest.Files)
	}

	if _, err := Verify(into); err != nil {
		t.Fatalf("a fresh backup does not verify: %v", err)
	}

	fresh := filepath.Join(root, "restored")
	if _, err := Restore(ctx, into, fresh, "0.0.1-test"); err != nil {
		t.Fatalf("restore into an empty directory failed: %v", err)
	}

	restored, err := store.Open(ctx, filepath.Join(fresh, "rss-expert.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	accounts := identity.NewStore(restored)
	owner, err := accounts.Owner(ctx)
	if err != nil {
		t.Fatalf("the owner did not survive the restore: %v", err)
	}
	if owner.Email != email {
		t.Errorf("owner = %q, want %q", owner.Email, email)
	}
	if _, err := accounts.Authenticate(ctx, email, "a long enough password"); err != nil {
		t.Errorf("the owner cannot sign in after a restore: %v", err)
	}

	posts := publish.NewStore(restored, "example.org")
	post, err := posts.ByGUID(ctx, guid)
	if err != nil {
		t.Fatalf("the post did not survive: %v", err)
	}
	if post.Markdown != "This has to survive a restore." {
		t.Errorf("the post came back as %q", post.Markdown)
	}
}

func TestRestoreRefusesToOverwriteALiveDatabase(t *testing.T) {
	ctx := context.Background()

	db, root := liveInstance(t)
	seed(t, db)

	into := filepath.Join(root, "backup")
	if _, err := Take(ctx, db, into, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, into, filepath.Join(root, "data"), "0.0.1-test"); !errors.Is(err, ErrTargetNotEmpty) {
		t.Errorf("restore was willing to write over a live database: %v", err)
	}
}

func TestACorruptedBackupIsRefused(t *testing.T) {
	ctx := context.Background()

	db, root := liveInstance(t)
	seed(t, db)

	into := filepath.Join(root, "backup")
	if _, err := Take(ctx, db, into, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(into, "rss-expert.db")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)/2] ^= 0xff
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Verify(into); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a flipped byte was not caught: %v", err)
	}
	if _, err := Restore(ctx, into, filepath.Join(root, "restored"), "0.0.1-test"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a corrupt backup was restored anyway: %v", err)
	}
}

func TestAMissingFileIsCaught(t *testing.T) {
	ctx := context.Background()

	db, root := liveInstance(t)
	seed(t, db)

	into := filepath.Join(root, "backup")
	if _, err := Take(ctx, db, into, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(into, "rss-expert.db")); err != nil {
		t.Fatal(err)
	}

	if _, err := Verify(into); !errors.Is(err, ErrManifestMismatch) {
		t.Errorf("a backup missing its database verified anyway: %v", err)
	}
}

func TestADirectoryWithoutAManifestIsNotABackup(t *testing.T) {
	_, root := liveInstance(t)
	if _, err := Verify(filepath.Join(root, "data")); !errors.Is(err, ErrNotABackup) {
		t.Errorf("a plain data directory passed as a backup: %v", err)
	}
}

func TestTheBackupIsAConsistentSnapshotNotACopy(t *testing.T) {
	ctx := context.Background()

	db, root := liveInstance(t)
	seed(t, db)

	into := filepath.Join(root, "backup")
	if _, err := Take(ctx, db, into, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}

	for _, stray := range []string{"rss-expert.db-wal", "rss-expert.db-shm"} {
		if _, err := os.Stat(filepath.Join(into, stray)); err == nil {
			t.Errorf("%s was copied into the backup; vacuum into should leave none", stray)
		}
	}
}
