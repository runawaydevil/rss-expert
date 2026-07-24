package activitypub

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

const KeyBits = 2048

var (
	ErrNoKey     = errors.New("activitypub: that account has no key yet")
	ErrNotRSA    = errors.New("activitypub: that key is not RSA")
	ErrUnreadPEM = errors.New("activitypub: that is not a readable PEM block")
)

type KeyPair struct {
	Private    *rsa.PrivateKey
	PrivatePEM string
	PublicPEM  string
}

func NewKeyPair() (*KeyPair, error) {
	key, err := rsa.GenerateKey(rand.Reader, KeyBits)
	if err != nil {
		return nil, fmt.Errorf("activitypub: generate key: %w", err)
	}

	private := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: mustMarshalPrivate(key),
	})

	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("activitypub: marshal public key: %w", err)
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})

	return &KeyPair{
		Private:    key,
		PrivatePEM: string(private),
		PublicPEM:  string(public),
	}, nil
}

func mustMarshalPrivate(key *rsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return x509.MarshalPKCS1PrivateKey(key)
	}
	return der
}

func ParsePrivateKey(text string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, ErrUnreadPEM
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, ErrNotRSA
		}
		return rsaKey, nil
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrUnreadPEM
	}
	return key, nil
}

func ParsePublicKey(text string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, ErrUnreadPEM
	}

	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, ErrNotRSA
		}
		return rsaKey, nil
	}

	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, ErrUnreadPEM
	}
	return key, nil
}
