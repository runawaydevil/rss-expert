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
	"strconv"
	"strings"
	"time"
)

const (
	SignatureLabel = "rss"
	Algorithm9421  = "rsa-v1_5-sha256"
	digestName     = "sha-256"
)

var (
	ErrNoSignatureInput = errors.New("activitypub: the request carries no Signature-Input")
	ErrUnknownAlgorithm = errors.New("activitypub: that signature algorithm is not one we verify")
	ErrDerivedComponent = errors.New("activitypub: the signature covers a component we cannot rebuild")
)

var postMustCover9421 = []string{"@method", "@target-uri", "content-digest"}

type Signature9421 struct {
	Label      string
	Components []string
	KeyID      string
	Algorithm  string
	Created    int64
	Expires    int64
	Raw        []byte
}

func ContentDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return digestName + "=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
}

func componentList(components []string) string {
	quoted := make([]string, len(components))
	for i, name := range components {
		quoted[i] = `"` + name + `"`
	}
	return "(" + strings.Join(quoted, " ") + ")"
}

func componentValue(req *http.Request, name string) (string, error) {
	switch name {
	case "@method":
		return strings.ToUpper(req.Method), nil
	case "@target-uri":
		return targetURI(req), nil
	case "@authority":
		return strings.ToLower(authorityOf(req)), nil
	case "@path":
		path := req.URL.EscapedPath()
		if path == "" {
			path = "/"
		}
		return path, nil
	case "@query":
		if req.URL.RawQuery == "" {
			return "?", nil
		}
		return "?" + req.URL.RawQuery, nil
	}
	if strings.HasPrefix(name, "@") {
		return "", fmt.Errorf("%w: %s", ErrDerivedComponent, name)
	}

	value := req.Header.Get(name)
	if value == "" {
		return "", fmt.Errorf("%w: %s", ErrMissingHeader, name)
	}
	return strings.TrimSpace(value), nil
}

func authorityOf(req *http.Request) string {
	if req.Host != "" {
		return req.Host
	}
	if host := req.Header.Get("Host"); host != "" {
		return host
	}
	return req.URL.Host
}

func targetURI(req *http.Request) string {
	scheme := req.URL.Scheme
	if scheme == "" {
		scheme = "https"
		if req.TLS == nil && req.URL.Host == "" {
			if forwarded := req.Header.Get("X-Forwarded-Proto"); forwarded != "" {
				scheme = strings.ToLower(forwarded)
			}
		}
	}

	target := req.URL.EscapedPath()
	if target == "" {
		target = "/"
	}
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	return scheme + "://" + strings.ToLower(authorityOf(req)) + target
}

func signatureParams(sig *Signature9421) string {
	params := componentList(sig.Components) +
		";created=" + strconv.FormatInt(sig.Created, 10)
	if sig.Expires > 0 {
		params += ";expires=" + strconv.FormatInt(sig.Expires, 10)
	}
	params += ";keyid=" + strconv.Quote(sig.KeyID)
	if sig.Algorithm != "" {
		params += ";alg=" + strconv.Quote(sig.Algorithm)
	}
	return params
}

func SignatureBase(req *http.Request, sig *Signature9421) (string, error) {
	lines := make([]string, 0, len(sig.Components)+1)
	for _, name := range sig.Components {
		value, err := componentValue(req, name)
		if err != nil {
			return "", err
		}
		lines = append(lines, `"`+name+`": `+value)
	}
	lines = append(lines, `"@signature-params": `+signatureParams(sig))
	return strings.Join(lines, "\n"), nil
}

func Sign9421(req *http.Request, keyID string, key *rsa.PrivateKey, body []byte) error {
	components := []string{"@method", "@target-uri", "@authority", "@path"}
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Digest", ContentDigest(body))
		components = append(components, "content-digest", "content-type")
	}
	if req.Header.Get("Date") == "" {
		req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	}

	sig := &Signature9421{
		Label:      SignatureLabel,
		Components: components,
		KeyID:      keyID,
		Algorithm:  Algorithm9421,
		Created:    time.Now().UTC().Unix(),
	}

	base, err := SignatureBase(req, sig)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(base))
	raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return fmt.Errorf("activitypub: sign: %w", err)
	}

	req.Header.Set("Signature-Input", sig.Label+"="+signatureParams(sig))
	req.Header.Set("Signature",
		sig.Label+"=:"+base64.StdEncoding.EncodeToString(raw)+":")
	return nil
}

func ParseSignature9421(input, signature string) (*Signature9421, error) {
	if strings.TrimSpace(input) == "" {
		return nil, ErrNoSignatureInput
	}

	label, rest, ok := strings.Cut(strings.TrimSpace(input), "=")
	if !ok {
		return nil, ErrMalformed
	}
	label = strings.TrimSpace(label)

	open := strings.Index(rest, "(")
	close := strings.Index(rest, ")")
	if open < 0 || close < open {
		return nil, ErrMalformed
	}

	sig := &Signature9421{Label: label}
	for _, field := range strings.Fields(rest[open+1 : close]) {
		name := strings.Trim(field, `"`)
		if name == "" {
			return nil, ErrMalformed
		}
		sig.Components = append(sig.Components, strings.ToLower(name))
	}
	if len(sig.Components) == 0 {
		return nil, ErrMalformed
	}

	for _, param := range strings.Split(rest[close+1:], ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "keyid":
			sig.KeyID = value
		case "alg":
			sig.Algorithm = value
		case "created":
			sig.Created, _ = strconv.ParseInt(value, 10, 64)
		case "expires":
			sig.Expires, _ = strconv.ParseInt(value, 10, 64)
		}
	}

	if sig.KeyID == "" {
		return nil, ErrNoKeyID
	}
	if sig.Algorithm != "" && sig.Algorithm != Algorithm9421 {
		return nil, ErrUnknownAlgorithm
	}

	raw, err := signatureBytes(signature, label)
	if err != nil {
		return nil, err
	}
	sig.Raw = raw
	return sig, nil
}

func signatureBytes(header, label string) ([]byte, error) {
	for _, part := range splitParams(header) {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(name) != label {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), ":")
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, ErrMalformed
		}
		if len(raw) == 0 {
			return nil, ErrMalformed
		}
		return raw, nil
	}
	return nil, ErrMalformed
}

func Verify9421(req *http.Request, body []byte, key *rsa.PublicKey, sig *Signature9421) error {
	if req.Method == http.MethodPost {
		if err := coversAll(sig.Components, postMustCover9421); err != nil {
			return err
		}
		if !sameDigest(req.Header.Get("Content-Digest"), body) {
			return ErrBadDigest
		}
	} else if err := coversAll(sig.Components, []string{"@method", "@target-uri"}); err != nil {
		return err
	}

	if sig.Created == 0 {
		return ErrStale
	}
	created := time.Unix(sig.Created, 0)
	if drift := time.Since(created); drift > ClockSkew || drift < -ClockSkew {
		return ErrStale
	}
	if sig.Expires > 0 && time.Now().After(time.Unix(sig.Expires, 0)) {
		return ErrStale
	}

	base, err := SignatureBase(req, sig)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(base))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig.Raw); err != nil {
		return ErrBadSignature
	}
	return nil
}

func sameDigest(header string, body []byte) bool {
	if header == "" {
		return false
	}
	want := ContentDigest(body)
	for _, part := range strings.Split(header, ",") {
		if strings.TrimSpace(part) == want {
			return true
		}
	}
	return false
}

const (
	SigningCavage = "cavage"
	Signing9421   = "rfc9421"
)

type IncomingSignature struct {
	KeyID  string
	Scheme string
	cavage *Signature
	rfc    *Signature9421
}

func ReadSignature(req *http.Request) (*IncomingSignature, error) {
	if input := req.Header.Get("Signature-Input"); strings.TrimSpace(input) != "" {
		sig, err := ParseSignature9421(input, req.Header.Get("Signature"))
		if err != nil {
			return nil, err
		}
		return &IncomingSignature{KeyID: sig.KeyID, Scheme: Signing9421, rfc: sig}, nil
	}

	sig, err := ParseSignature(req.Header.Get("Signature"))
	if err != nil {
		return nil, err
	}
	return &IncomingSignature{KeyID: sig.KeyID, Scheme: SigningCavage, cavage: sig}, nil
}

func (s *IncomingSignature) Verify(req *http.Request, body []byte, key *rsa.PublicKey) error {
	if s.rfc != nil {
		return Verify9421(req, body, key, s.rfc)
	}
	return Verify(req, body, key, s.cavage)
}

func signAs(scheme string, req *http.Request, keyID string, key *rsa.PrivateKey, body []byte) error {
	if scheme == Signing9421 {
		return Sign9421(req, keyID, key, body)
	}
	return Sign(req, keyID, key, body)
}

func theOther(scheme string) string {
	if scheme == Signing9421 {
		return SigningCavage
	}
	return Signing9421
}
