package activitypub

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signed9421(t *testing.T, key *rsa.PrivateKey, keyID string, body []byte) *http.Request {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost,
		"https://remote.example/users/bob/inbox", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/activity+json")

	if err := Sign9421(req, keyID, key, body); err != nil {
		t.Fatal(err)
	}
	return req
}

func verify9421Of(t *testing.T, req *http.Request, body []byte, pub *rsa.PublicKey) error {
	t.Helper()
	sig, err := ParseSignature9421(req.Header.Get("Signature-Input"), req.Header.Get("Signature"))
	if err != nil {
		return err
	}
	return Verify9421(req, body, pub, sig)
}

func TestTheContentDigestMatchesRFC9530(t *testing.T) {
	if got := ContentDigest([]byte("hello world")); got != "sha-256=:uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=:" {
		t.Errorf("ContentDigest = %q", got)
	}
	if got := ContentDigest(nil); got != "sha-256=:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=:" {
		t.Errorf("the digest of an empty body = %q", got)
	}
}

func TestTheSignatureBaseIsBuiltExactlyAsRFC9421Says(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost,
		"https://remote.example/users/bob/inbox", strings.NewReader("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Content-Digest", ContentDigest([]byte("hello world")))

	base, err := SignatureBase(req, &Signature9421{
		Label:      SignatureLabel,
		Components: []string{"@method", "@target-uri", "@authority", "@path", "content-digest", "content-type"},
		KeyID:      "https://us.example/users/a#main-key",
		Algorithm:  Algorithm9421,
		Created:    1618884473,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `"@method": POST` + "\n" +
		`"@target-uri": https://remote.example/users/bob/inbox` + "\n" +
		`"@authority": remote.example` + "\n" +
		`"@path": /users/bob/inbox` + "\n" +
		`"content-digest": sha-256=:uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=:` + "\n" +
		`"content-type": application/activity+json` + "\n" +
		`"@signature-params": ("@method" "@target-uri" "@authority" "@path" "content-digest" "content-type");created=1618884473;keyid="https://us.example/users/a#main-key";alg="rsa-v1_5-sha256"`

	if base != want {
		t.Errorf("the signature base is not what the RFC describes.\n got:\n%s\nwant:\n%s", base, want)
	}
}

func TestAnHonest9421DeliveryVerifies(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	req := signed9421(t, key, "https://us.example/users/a#main-key", body)
	if err := verify9421Of(t, req, body, &key.PublicKey); err != nil {
		t.Fatalf("an honest delivery was refused: %v", err)
	}
	if req.Header.Get("Content-Digest") == "" {
		t.Error("the signed request carries no Content-Digest")
	}
	if !strings.HasPrefix(req.Header.Get("Signature"), SignatureLabel+"=:") {
		t.Errorf("the Signature header is not a labelled byte sequence: %q", req.Header.Get("Signature"))
	}
}

func TestEvery9421ForgeryIsRefused(t *testing.T) {
	key := testKey(t)
	other := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	for name, forge := range map[string]func() (*http.Request, []byte){
		"signed with another key": func() (*http.Request, []byte) {
			return signed9421(t, other, "https://us.example/users/a#main-key", body), body
		},
		"the body changed after signing": func() (*http.Request, []byte) {
			return signed9421(t, key, "https://us.example/users/a#main-key", body),
				[]byte(`{"type":"Delete"}`)
		},
		"a covered header rewritten": func() (*http.Request, []byte) {
			req := signed9421(t, key, "https://us.example/users/a#main-key", body)
			req.Header.Set("Content-Type", "text/plain")
			return req, body
		},
		"the path rewritten": func() (*http.Request, []byte) {
			req := signed9421(t, key, "https://us.example/users/a#main-key", body)
			req.URL.Path = "/users/mallory/inbox"
			return req, body
		},
		"the digest left out of the coverage": func() (*http.Request, []byte) {
			req := signed9421(t, key, "https://us.example/users/a#main-key", body)
			input := req.Header.Get("Signature-Input")
			req.Header.Set("Signature-Input",
				strings.Replace(input, ` "content-digest"`, "", 1))
			return req, body
		},
		"a stale created stamp": func() (*http.Request, []byte) {
			req := signed9421(t, key, "https://us.example/users/a#main-key", body)
			input := req.Header.Get("Signature-Input")
			stale := strconv.FormatInt(time.Now().Add(-2*ClockSkew).Unix(), 10)
			_, rest, _ := strings.Cut(input, ";created=")
			old, _, _ := strings.Cut(rest, ";")
			req.Header.Set("Signature-Input", strings.Replace(input, "created="+old, "created="+stale, 1))
			return req, body
		},
	} {
		req, sent := forge()
		if err := verify9421Of(t, req, sent, &key.PublicKey); err == nil {
			t.Errorf("%s verified", name)
		}
	}
}

func TestAn9421RequestWithoutASignatureIsRefused(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://remote.example/inbox", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSignature(req); !errors.Is(err, ErrNoSignature) {
		t.Errorf("an unsigned request gave %v", err)
	}
}

func TestAnUnknownAlgorithmIsRefused(t *testing.T) {
	input := `rss=("@method" "@target-uri");created=1618884473;keyid="k";alg="ed25519"`
	if _, err := ParseSignature9421(input, "rss=:AAAA:"); !errors.Is(err, ErrUnknownAlgorithm) {
		t.Errorf("an algorithm we do not implement gave %v", err)
	}
}

func TestAMalformedSignatureInputIsRefused(t *testing.T) {
	for name, input := range map[string]string{
		"no components": `rss=();created=1;keyid="k"`,
		"no key":        `rss=("@method");created=1`,
		"no list":       `rss=;created=1;keyid="k"`,
		"nonsense":      `not a signature input at all`,
	} {
		if _, err := ParseSignature9421(input, "rss=:AAAA:"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestReadSignaturePicksTheSchemeTheSenderUsed(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	modern := signed9421(t, key, "https://us.example/users/a#main-key", body)
	sig, err := ReadSignature(modern)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Scheme != Signing9421 {
		t.Errorf("a Signature-Input request was read as %q", sig.Scheme)
	}
	if err := sig.Verify(modern, body, &key.PublicKey); err != nil {
		t.Errorf("the modern signature did not verify: %v", err)
	}

	legacy := signedPost(t, key, "https://us.example/users/a#main-key", body)
	sig, err = ReadSignature(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Scheme != SigningCavage {
		t.Errorf("a draft-cavage request was read as %q", sig.Scheme)
	}
	if err := sig.Verify(legacy, body, &key.PublicKey); err != nil {
		t.Errorf("the draft signature did not verify: %v", err)
	}
}

func TestASignatureCoveringSomethingWeCannotRebuildIsRefused(t *testing.T) {
	key := testKey(t)
	body := []byte(`{}`)
	req := signed9421(t, key, "https://us.example/users/a#main-key", body)

	input := req.Header.Get("Signature-Input")
	req.Header.Set("Signature-Input", strings.Replace(input, `"@path"`, `"@path" "@status"`, 1))

	if err := verify9421Of(t, req, body, &key.PublicKey); !errors.Is(err, ErrDerivedComponent) {
		t.Errorf("a signature over @status gave %v", err)
	}
}
