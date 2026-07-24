package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func seedMedia(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUploadsTravelWithTheBackup(t *testing.T) {
	ctx := context.Background()
	db, root := liveInstance(t)
	dataDir := filepath.Join(root, "data")

	mediaDir := filepath.Join(dataDir, "media")
	seedMedia(t, mediaDir, map[string]string{
		"ab/abcd": "the first upload",
		"cd/cdef": "the second upload",
	})

	into := filepath.Join(root, "backup")
	manifest, err := TakeWithMedia(ctx, db, into, "0.0.1-test", mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Media) != 2 {
		t.Fatalf("%d uploads recorded, want 2", len(manifest.Media))
	}
	if manifest.MediaReused != 0 {
		t.Errorf("%d reused on a first run", manifest.MediaReused)
	}

	body, err := os.ReadFile(filepath.Join(into, "media", "ab", "abcd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "the first upload" {
		t.Errorf("copied body = %q", body)
	}

	if _, err := Verify(into); err != nil {
		t.Errorf("a backup carrying uploads does not verify: %v", err)
	}
}

func TestASecondBackupOnlyCopiesWhatIsNew(t *testing.T) {
	ctx := context.Background()
	db, root := liveInstance(t)
	dataDir := filepath.Join(root, "data")

	mediaDir := filepath.Join(dataDir, "media")
	seedMedia(t, mediaDir, map[string]string{"ab/abcd": "the first upload"})

	into := filepath.Join(root, "backup")
	if _, err := TakeWithMedia(ctx, db, into, "0.0.1-test", mediaDir); err != nil {
		t.Fatal(err)
	}

	seedMedia(t, mediaDir, map[string]string{"cd/cdef": "arrived after the first backup"})

	manifest, err := TakeWithMedia(ctx, db, into, "0.0.1-test", mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Media) != 2 {
		t.Fatalf("%d uploads recorded, want 2", len(manifest.Media))
	}
	if manifest.MediaReused != 1 {
		t.Errorf("reused = %d, want 1; the unchanged upload was copied again", manifest.MediaReused)
	}
}

func TestCorruptUploadFailsVerification(t *testing.T) {
	ctx := context.Background()
	db, root := liveInstance(t)
	dataDir := filepath.Join(root, "data")

	mediaDir := filepath.Join(dataDir, "media")
	seedMedia(t, mediaDir, map[string]string{"ab/abcd": "the first upload"})

	into := filepath.Join(root, "backup")
	if _, err := TakeWithMedia(ctx, db, into, "0.0.1-test", mediaDir); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(into, "media", "ab", "abcd"), []byte("rot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(into); err == nil {
		t.Error("a damaged upload passed verification")
	}
}

func TestRestoreBringsUploadsBack(t *testing.T) {
	ctx := context.Background()
	db, root := liveInstance(t)
	dataDir := filepath.Join(root, "data")

	mediaDir := filepath.Join(dataDir, "media")
	seedMedia(t, mediaDir, map[string]string{"ab/abcd": "the first upload"})

	into := filepath.Join(root, "backup")
	if _, err := TakeWithMedia(ctx, db, into, "0.0.1-test", mediaDir); err != nil {
		t.Fatal(err)
	}
	db.Close()

	fresh := filepath.Join(root, "restored")
	if _, err := Restore(ctx, into, fresh, "0.0.1-test"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(fresh, "media", "ab", "abcd"))
	if err != nil {
		t.Fatalf("the upload did not come back: %v", err)
	}
	if string(body) != "the first upload" {
		t.Errorf("restored body = %q", body)
	}
}
