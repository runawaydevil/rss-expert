package activitypub

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ClockSkew     = 12 * time.Hour
	digestPrefix  = "SHA-256="
	requestTarget = "(request-target)"
)

var (
	ErrNoSignature   = errors.New("activitypub: the request carries no signature")
	ErrMalformed     = errors.New("activitypub: the signature header cannot be read")
	ErrNoKeyID       = errors.New("activitypub: the signature names no key")
	ErrMissingHeader = errors.New("activitypub: the signature covers a header the request does not carry")
	ErrUncovered     = errors.New("activitypub: the signature leaves a header unprotected")
	ErrBadDigest     = errors.New("activitypub: the digest does not match the body")
	ErrStale         = errors.New("activitypub: the request date is outside the accepted window")
	ErrBadSignature  = errors.New("activitypub: the signature does not verify against that key")
)

var postMustCover = []string{requestTarget, "host", "date", "digest"}

type Signature struct {
	KeyID     string
	Algorithm string
	Headers   []string
	Raw       []byte
}

func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return digestPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

func SigningString(method, target string, header http.Header, covered []string) (string, error) {
	lines := make([]string, 0, len(covered))
	for _, name := range covered {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == requestTarget {
			lines = append(lines, requestTarget+": "+strings.ToLower(method)+" "+target)
			continue
		}
		value := header.Get(name)
		if value == "" {
			return "", fmt.Errorf("%w: %s", ErrMissingHeader, name)
		}
		lines = append(lines, name+": "+strings.TrimSpace(value))
	}
	return strings.Join(lines, "\n"), nil
}

func Sign(req *http.Request, keyID string, key *rsa.PrivateKey, body []byte) error {
	if req.Header.Get("Date") == "" {
		req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	}
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	covered := []string{requestTarget, "host", "date"}
	if req.Method == http.MethodPost {
		req.Header.Set("Digest", Digest(body))
		covered = append(covered, "digest", "content-type")
	}

	signing, err := SigningString(req.Method, targetOf(req), req.Header, covered)
	if err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(signing))
	raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return fmt.Errorf("activitypub: sign: %w", err)
	}

	req.Header.Set("Signature", fmt.Sprintf(
		`keyId=%q,algorithm="rsa-sha256",headers=%q,signature=%q`,
		keyID, strings.Join(covered, " "), base64.StdEncoding.EncodeToString(raw)))
	return nil
}

func ParseSignature(header string) (*Signature, error) {
	if strings.TrimSpace(header) == "" {
		return nil, ErrNoSignature
	}

	sig := &Signature{}
	for _, part := range splitParams(header) {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "keyid":
			sig.KeyID = value
		case "algorithm":
			sig.Algorithm = value
		case "headers":
			sig.Headers = strings.Fields(strings.ToLower(value))
		case "signature":
			raw, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				return nil, ErrMalformed
			}
			sig.Raw = raw
		}
	}

	if sig.KeyID == "" {
		return nil, ErrNoKeyID
	}
	if len(sig.Raw) == 0 || len(sig.Headers) == 0 {
		return nil, ErrMalformed
	}
	return sig, nil
}

func splitParams(header string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range header {
		switch r {
		case '"':
			depth ^= 1
		case ',':
			if depth == 0 {
				out = append(out, header[start:i])
				start = i + 1
			}
		}
	}
	return append(out, header[start:])
}

func Verify(req *http.Request, body []byte, key *rsa.PublicKey, sig *Signature) error {
	if req.Method == http.MethodPost {
		if err := coversAll(sig.Headers, postMustCover); err != nil {
			return err
		}
		if req.Header.Get("Digest") != Digest(body) {
			return ErrBadDigest
		}
	} else if err := coversAll(sig.Headers, []string{requestTarget, "host", "date"}); err != nil {
		return err
	}

	when, err := http.ParseTime(req.Header.Get("Date"))
	if err != nil {
		return ErrStale
	}
	if drift := time.Since(when); drift > ClockSkew || drift < -ClockSkew {
		return ErrStale
	}

	signing, err := SigningString(req.Method, targetOf(req), requestHeaders(req), sig.Headers)
	if err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(signing))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig.Raw); err != nil {
		return ErrBadSignature
	}
	return nil
}

func coversAll(covered, required []string) error {
	have := make(map[string]bool, len(covered))
	for _, name := range covered {
		have[strings.ToLower(name)] = true
	}
	for _, name := range required {
		if !have[name] {
			return fmt.Errorf("%w: %s", ErrUncovered, name)
		}
	}
	return nil
}

func requestHeaders(req *http.Request) http.Header {
	header := req.Header.Clone()
	if header.Get("Host") == "" && req.Host != "" {
		header.Set("Host", req.Host)
	}
	return header
}

func targetOf(req *http.Request) string {
	target := req.URL.EscapedPath()
	if target == "" {
		target = "/"
	}
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	return target
}
