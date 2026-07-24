package activitypub

import (
	"strings"
	"testing"
)

func TestAKeyPairRoundTripsThroughPEM(t *testing.T) {
	pair, err := NewKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(pair.PublicPEM, "-----BEGIN PUBLIC KEY-----") {
		t.Errorf("the public PEM does not look like one:\n%s", pair.PublicPEM)
	}
	if strings.Contains(pair.PublicPEM, "PRIVATE") {
		t.Fatal("the public PEM carries private material")
	}

	private, err := ParsePrivateKey(pair.PrivatePEM)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ParsePublicKey(pair.PublicPEM)
	if err != nil {
		t.Fatal(err)
	}

	if private.N.Cmp(public.N) != 0 {
		t.Error("the parsed halves do not belong to the same key")
	}
	if private.N.BitLen() < KeyBits {
		t.Errorf("key is %d bits, want at least %d", private.N.BitLen(), KeyBits)
	}
}

func TestASignatureVerifiesAcrossTheSerialisedKey(t *testing.T) {
	pair, err := NewKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	private, err := ParsePrivateKey(pair.PrivatePEM)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ParsePublicKey(pair.PublicPEM)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"type":"Create"}`)
	req := signedPost(t, private, "https://us.example/users/alice#main-key", body)

	if err := verifyOf(t, req, body, public); err != nil {
		t.Errorf("a key that went through PEM no longer verifies: %v", err)
	}
}

func TestNonsenseKeysAreRefused(t *testing.T) {
	for name, text := range map[string]string{
		"empty":     "",
		"not pem":   "just some words",
		"truncated": "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n",
	} {
		if _, err := ParsePublicKey(text); err == nil {
			t.Errorf("%s was accepted as a public key", name)
		}
		if _, err := ParsePrivateKey(text); err == nil {
			t.Errorf("%s was accepted as a private key", name)
		}
	}
}
