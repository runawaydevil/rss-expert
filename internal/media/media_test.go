package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/store"
)

func testStore(t *testing.T, quota int64) (*Store, *identity.Account) {
	t.Helper()
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "rss-expert-media")
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

	account, err := identity.NewStore(db).Create(ctx, "alice@example.org", "a long enough password", identity.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	return New(db, Options{Root: filepath.Join(dir, "media"), Quota: quota}), account
}

func plainJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for x := 0; x < 24; x++ {
		for y := 0; y < 16; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 10), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func withEXIF(t *testing.T, body []byte, payload string) []byte {
	t.Helper()
	if len(body) < 2 {
		t.Fatal("not a jpeg")
	}

	segment := append([]byte("Exif\x00\x00"), []byte(payload)...)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(segment)+2))

	out := make([]byte, 0, len(body)+len(segment)+4)
	out = append(out, body[:2]...)
	out = append(out, 0xff, 0xe1)
	out = append(out, length...)
	out = append(out, segment...)
	out = append(out, body[2:]...)
	return out
}

func TestEXIFIsRemovedFromJPEG(t *testing.T) {
	const gps = "GPSLatitude=-23.5505 GPSLongitude=-46.6333 CameraSerial=12345"

	original := plainJPEG(t)
	tagged := withEXIF(t, original, gps)

	if !bytes.Contains(tagged, []byte(gps)) {
		t.Fatal("the fixture does not actually carry the location")
	}

	cleaned, changed, err := Strip("image/jpeg", tagged)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("stripping reported no change on a file that had EXIF")
	}
	if bytes.Contains(cleaned, []byte("GPSLatitude")) {
		t.Fatal("the photo still carries its GPS coordinates")
	}
	if bytes.Contains(cleaned, []byte("CameraSerial")) {
		t.Error("the camera serial number survived")
	}

	if _, _, err := image.Decode(bytes.NewReader(cleaned)); err != nil {
		t.Fatalf("stripping broke the image: %v", err)
	}
}

func TestStrippingIsLosslessNotAReEncode(t *testing.T) {
	original := plainJPEG(t)
	cleaned, _, err := Strip("image/jpeg", original)
	if err != nil {
		t.Fatal(err)
	}

	before, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := image.Decode(bytes.NewReader(cleaned))
	if err != nil {
		t.Fatal(err)
	}

	for x := 0; x < 24; x++ {
		for y := 0; y < 16; y++ {
			if before.At(x, y) != after.At(x, y) {
				t.Fatalf("pixel %d,%d changed; stripping must not re-encode", x, y)
			}
		}
	}
}

func TestPNGTextChunksAreRemoved(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	body := buf.Bytes()
	chunk := makePNGChunk("tEXt", []byte("Comment\x00taken at home"))
	withText := append(append(append([]byte{}, body[:33]...), chunk...), body[33:]...)

	cleaned, changed, err := Strip("image/png", withText)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("no change reported for a png carrying a tEXt chunk")
	}
	if bytes.Contains(cleaned, []byte("taken at home")) {
		t.Error("the png comment survived")
	}
	if _, err := png.Decode(bytes.NewReader(cleaned)); err != nil {
		t.Fatalf("stripping broke the png: %v", err)
	}
}

func makePNGChunk(name string, data []byte) []byte {
	out := make([]byte, 0, len(data)+12)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(data)))
	out = append(out, length...)
	out = append(out, []byte(name)...)
	out = append(out, data...)

	crc := crc32Of(append([]byte(name), data...))
	sum := make([]byte, 4)
	binary.BigEndian.PutUint32(sum, crc)
	return append(out, sum...)
}

func TestUploadStripsAndRecords(t *testing.T) {
	s, account := testStore(t, DefaultQuota)
	ctx := context.Background()

	tagged := withEXIF(t, plainJPEG(t), "GPSLatitude=-23.5505")

	file, err := s.Put(ctx, account.ID, "../../etc/holiday.jpg", tagged, "A test pattern")
	if err != nil {
		t.Fatal(err)
	}
	if !file.Stripped {
		t.Error("the upload was not marked as stripped")
	}
	if file.MediaType != "image/jpeg" {
		t.Errorf("media type = %q", file.MediaType)
	}
	if file.Width != 24 || file.Height != 16 {
		t.Errorf("dimensions = %dx%d", file.Width, file.Height)
	}
	if file.Alt != "A test pattern" {
		t.Errorf("alt = %q", file.Alt)
	}
	if file.Name != "holiday.jpg" {
		t.Errorf("stored name = %q; a path must never survive an upload", file.Name)
	}

	reader, err := s.Open(file.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	stored := make([]byte, file.Bytes)
	if _, err := reader.Read(stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("GPSLatitude")) {
		t.Fatal("the file on disk still carries the coordinates")
	}
}

func TestOnlyKnownKindsAreAccepted(t *testing.T) {
	s, account := testStore(t, DefaultQuota)
	ctx := context.Background()

	for name, body := range map[string][]byte{
		"a script": []byte("#!/bin/sh\nrm -rf /\n"),
		"html":     []byte("<html><body><script>alert(1)</script></body></html>"),
		"a pdf":    []byte("%PDF-1.4\n%aaa\n"),
		"empty":    {},
		"nonsense": []byte("just some words that are not a file"),
	} {
		if _, err := s.Put(ctx, account.ID, name, body, ""); err == nil {
			t.Errorf("%s was accepted as media", name)
		}
	}
}

func TestAnImageThatDoesNotDecodeIsRefused(t *testing.T) {
	s, account := testStore(t, DefaultQuota)

	liar := append([]byte{0xff, 0xd8, 0xff}, []byte("this is not really a jpeg at all")...)
	if _, err := s.Put(context.Background(), account.ID, "liar.jpg", liar, ""); !errors.Is(err, ErrNotAnImage) {
		t.Errorf("a file claiming to be a jpeg but failing to decode gave %v", err)
	}
}

func TestQuotaIsEnforced(t *testing.T) {
	body := plainJPEG(t)
	s, account := testStore(t, int64(len(body))+10)
	ctx := context.Background()

	if _, err := s.Put(ctx, account.ID, "first.jpg", body, ""); err != nil {
		t.Fatal(err)
	}

	remaining, err := s.Remaining(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if remaining > 10 {
		t.Errorf("remaining = %d after filling the quota", remaining)
	}

	other := withEXIF(t, body, "padding to make it different and bigger than the room left")
	if _, err := s.Put(ctx, account.ID, "second.jpg", other, ""); !errors.Is(err, ErrQuota) {
		t.Errorf("the second upload gave %v, want ErrQuota", err)
	}
}

func TestTheSameFileTwiceIsStoredOnce(t *testing.T) {
	s, account := testStore(t, DefaultQuota)
	ctx := context.Background()
	body := plainJPEG(t)

	first, err := s.Put(ctx, account.ID, "a.jpg", body, "first alt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(ctx, account.ID, "b.jpg", body, "second alt")
	if err != nil {
		t.Fatal(err)
	}

	if first.ID != second.ID {
		t.Errorf("the same bytes made two records: %d and %d", first.ID, second.ID)
	}
	if second.Alt != "second alt" {
		t.Errorf("alt = %q, want the newer one", second.Alt)
	}

	files, err := s.ForAccount(ctx, account.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("%d files listed, want 1", len(files))
	}
}

func TestOpenRefusesAnythingThatIsNotAHash(t *testing.T) {
	s, _ := testStore(t, DefaultQuota)
	for _, bad := range []string{
		"", "../../../etc/passwd", "not-a-hash",
		"../" + "a", "0000000000000000000000000000000000000000000000000000000000000000/../x",
	} {
		if _, err := s.Open(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%q) gave %v, want ErrNotFound", bad, err)
		}
	}
}

func crc32Of(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}

func TestFilesComeBackAttachedToTheirPost(t *testing.T) {
	s, account := testStore(t, DefaultQuota)
	ctx := context.Background()

	first, err := s.Put(ctx, account.ID, "one.jpg", plainJPEG(t), "the first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(ctx, account.ID, "two.png", plainPNG(t), "the second")
	if err != nil {
		t.Fatal(err)
	}

	post := seedPost(t, s, account.ID)
	if err := s.Attach(ctx, post, second.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach(ctx, post, first.ID, 0); err != nil {
		t.Fatal(err)
	}

	files, err := s.ForPost(ctx, post)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("%d files on the post, want 2", len(files))
	}
	if files[0].ID != first.ID {
		t.Error("the files came back out of the order they were attached in")
	}
	if files[0].Alt != "the first" {
		t.Errorf("alt = %q; the description did not survive the join", files[0].Alt)
	}
}

func plainPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 6, 6))
	img.Set(1, 1, color.RGBA{10, 20, 30, 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var seeded int

func seedPost(t *testing.T, s *Store, accountID int64) int64 {
	t.Helper()
	seeded++
	result, err := s.db.Write.ExecContext(context.Background(),
		`insert into post (account_id, guid, markdown, html, published_at)
		 values (?, ?, 'text', '<p>text</p>', ?)`,
		accountID, fmt.Sprintf("https://example.org/p/%d", seeded), 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOneQueryCarriesEveryPostsFiles(t *testing.T) {
	s, account := testStore(t, DefaultQuota)
	ctx := context.Background()

	first, err := s.Put(ctx, account.ID, "one.jpg", plainJPEG(t), "the first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(ctx, account.ID, "two.png", plainPNG(t), "the second")
	if err != nil {
		t.Fatal(err)
	}

	postA := seedPost(t, s, account.ID)
	postB := seedPost(t, s, account.ID)

	if err := s.Attach(ctx, postA, first.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach(ctx, postA, second.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach(ctx, postB, second.ID, 0); err != nil {
		t.Fatal(err)
	}

	byPost, err := s.ForPosts(ctx, []int64{postA, postB, postA + postB + 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(byPost[postA]) != 2 {
		t.Errorf("%d files on the first post, want 2", len(byPost[postA]))
	}
	if byPost[postA][0].ID != first.ID {
		t.Error("the files came back out of the order they were attached in")
	}
	if len(byPost[postB]) != 1 {
		t.Errorf("%d files on the second post, want 1", len(byPost[postB]))
	}
	if _, ok := byPost[postA+postB+99]; ok {
		t.Error("a post with no files got an entry")
	}

	if empty, err := s.ForPosts(ctx, nil); err != nil || empty != nil {
		t.Errorf("asking for no posts gave %v, %v", empty, err)
	}
}
