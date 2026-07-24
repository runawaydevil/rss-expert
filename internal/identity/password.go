package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const MinPasswordLength = 12

var (
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrHashFormat       = errors.New("password hash is not in the expected format")
)

type argonParams struct {
	memoryKiB uint32
	passes    uint32
	threads   uint8
	keyLen    uint32
	saltLen   int
}

var current = argonParams{memoryKiB: 47104, passes: 1, threads: 1, keyLen: 32, saltLen: 16}

func HashPassword(plain string) (string, error) {
	if len([]rune(plain)) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	return hashWith(current, plain)
}

func hashWith(p argonParams, plain string) (string, error) {
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(plain), salt, p.passes, p.memoryKiB, p.threads, p.keyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.passes, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

func VerifyPassword(encoded, plain string) (bool, error) {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(plain), salt, p.passes, p.memoryKiB, p.threads, p.keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrHashFormat
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrHashFormat
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.passes, &p.threads); err != nil {
		return p, nil, nil, ErrHashFormat
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrHashFormat
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, ErrHashFormat
	}

	p.saltLen = len(salt)
	p.keyLen = uint32(len(sum))
	return p, salt, sum, nil
}
