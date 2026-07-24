package activitypub

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signedPost(t *testing.T, key *rsa.PrivateKey, keyID string, body []byte) *http.Request {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost,
		"https://remote.example/users/bob/inbox", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/activity+json")

	if err := Sign(req, keyID, key, body); err != nil {
		t.Fatal(err)
	}
	return req
}

func verifyOf(t *testing.T, req *http.Request, body []byte, pub *rsa.PublicKey) error {
	t.Helper()
	sig, err := ParseSignature(req.Header.Get("Signature"))
	if err != nil {
		return err
	}
	return Verify(req, body, pub, sig)
}

func TestTheSigningStringIsBuiltExactlyAsTheDraftSays(t *testing.T) {
	header := http.Header{}
	header.Set("Host", "remote.example")
	header.Set("Date", "Sun, 05 Jan 2014 21:31:40 GMT")
	header.Set("Digest", "SHA-256=X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE=")

	got, err := SigningString(http.MethodPost, "/users/bob/inbox", header,
		[]string{requestTarget, "host", "date", "digest"})
	if err != nil {
		t.Fatal(err)
	}

	want := "(request-target): post /users/bob/inbox\n" +
		"host: remote.example\n" +
		"date: Sun, 05 Jan 2014 21:31:40 GMT\n" +
		"digest: SHA-256=X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE="

	if got != want {
		t.Errorf("signing string:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTheDigestMatchesTheKnownVector(t *testing.T) {
	if got := Digest([]byte("hello world")); got != "SHA-256=uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=" {
		t.Errorf("digest of \"hello world\" = %q", got)
	}
	if got := Digest(nil); got != "SHA-256=47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=" {
		t.Errorf("digest of an empty body = %q", got)
	}
}

func TestOurOwnSignatureVerifies(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	req := signedPost(t, key, "https://us.example/users/alice#main-key", body)

	if got := req.Header.Get("Digest"); got != Digest(body) {
		t.Errorf("Digest header = %q", got)
	}
	if err := verifyOf(t, req, body, &key.PublicKey); err != nil {
		t.Fatalf("our own signature did not verify: %v", err)
	}

	sig, _ := ParseSignature(req.Header.Get("Signature"))
	if sig.KeyID != "https://us.example/users/alice#main-key" {
		t.Errorf("keyId = %q", sig.KeyID)
	}
	if err := coversAll(sig.Headers, postMustCover); err != nil {
		t.Errorf("a POST we signed leaves something unprotected: %v", err)
	}
}

func TestAnotherKeyDoesNotVerify(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	req := signedPost(t, testKey(t), "https://us.example/users/alice#main-key", body)

	stranger := testKey(t)
	if err := verifyOf(t, req, body, &stranger.PublicKey); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a signature checked against a stranger's key gave %v", err)
	}
}

func TestATamperedBodyIsRefused(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow","actor":"https://them.example/users/bob"}`)

	req := signedPost(t, key, "https://us.example/users/alice#main-key", body)

	swapped := []byte(`{"type":"Follow","actor":"https://evil.example/users/mallory"}`)
	if err := verifyOf(t, req, swapped, &key.PublicKey); !errors.Is(err, ErrBadDigest) {
		t.Errorf("a swapped body gave %v, want the digest to catch it", err)
	}
}

func TestATamperedHeaderIsRefused(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	req := signedPost(t, key, "https://us.example/users/alice#main-key", body)
	req.Header.Set("Date", time.Now().UTC().Add(-time.Minute).Format(http.TimeFormat))

	if err := verifyOf(t, req, body, &key.PublicKey); !errors.Is(err, ErrBadSignature) {
		t.Errorf("rewriting a signed header gave %v", err)
	}
}

func TestAPostThatLeavesTheDigestUnsignedIsRefused(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	req := signedPost(t, key, "https://us.example/users/alice#main-key", body)
	req.Header.Set("Signature", strings.Replace(
		req.Header.Get("Signature"), " digest content-type", "", 1))

	err := verifyOf(t, req, body, &key.PublicKey)
	if !errors.Is(err, ErrUncovered) {
		t.Errorf("a POST with the digest left out of the signature gave %v", err)
	}
}

func TestAnOldRequestIsRefused(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	req, err := http.NewRequest(http.MethodPost,
		"https://remote.example/users/bob/inbox", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Date", time.Now().UTC().Add(-48*time.Hour).Format(http.TimeFormat))

	if err := Sign(req, "https://us.example/users/alice#main-key", key, body); err != nil {
		t.Fatal(err)
	}
	if err := verifyOf(t, req, body, &key.PublicKey); !errors.Is(err, ErrStale) {
		t.Errorf("a request signed two days ago gave %v, want it refused as stale", err)
	}
}

func TestAnUnsignedRequestIsRefused(t *testing.T) {
	for name, header := range map[string]string{
		"empty":      "",
		"no keyId":   `algorithm="hs2019",headers="(request-target)",signature="AAAA"`,
		"no headers": `keyId="https://us.example/k",signature="AAAA"`,
		"nonsense":   "this is not a signature",
		"bad base64": `keyId="https://us.example/k",headers="date",signature="not base64!!"`,
	} {
		if _, err := ParseSignature(header); err == nil {
			t.Errorf("%s was accepted as a signature", name)
		}
	}
}

func TestTheKeyIdSurvivesCommasInsideQuotes(t *testing.T) {
	sig, err := ParseSignature(
		`keyId="https://us.example/users/a,b#main-key",algorithm="hs2019",` +
			`headers="(request-target) host date",signature="AAAA"`)
	if err != nil {
		t.Fatal(err)
	}
	if sig.KeyID != "https://us.example/users/a,b#main-key" {
		t.Errorf("keyId = %q; a comma inside quotes split the header", sig.KeyID)
	}
	if len(sig.Headers) != 3 {
		t.Errorf("headers = %v", sig.Headers)
	}
}
