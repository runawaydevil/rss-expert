package activitypub

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/runawaydevil/rss-expert/internal/safety"
)

const MaxDocumentBytes = 256 << 10

var (
	ErrRefused   = errors.New("activitypub: the far side refused the delivery")
	ErrRedirect  = errors.New("activitypub: a signed request may not be redirected")
	ErrNoActorAt = errors.New("activitypub: nothing that address answers as an actor")
)

type Client struct {
	fetcher *safety.Fetcher
	agent   string
}

type ClientOptions struct {
	UserAgent    string
	ReachPrivate bool
}

func NewClient(o ClientOptions) *Client {
	if o.UserAgent == "" {
		o.UserAgent = "rss-expert"
	}
	return &Client{
		agent: o.UserAgent,
		fetcher: safety.New(safety.Options{
			MaxBytes:          MaxDocumentBytes,
			NoRedirects:       true,
			UserAgent:         o.UserAgent,
			AllowPrivateAddrs: o.ReachPrivate,
		}),
	}
}

type Identity struct {
	KeyID   string
	Key     *rsa.PrivateKey
	Signing string
}

func (c *Client) FetchActor(ctx context.Context, uri string, as Identity) (*Actor, error) {
	body, err := c.get(ctx, uri, as)
	if err != nil {
		return nil, err
	}

	actor, err := ParseActor(body)
	if err != nil {
		return nil, err
	}
	if !SameOrigin(uri, actor.ID) {
		return nil, ErrOriginMismatch
	}
	return actor, nil
}

func (c *Client) Finger(ctx context.Context, address Address, as Identity) (string, error) {
	body, err := c.get(ctx, FingerURL(address.Host, address.String()), as)
	if err != nil {
		return "", err
	}

	var jrd JRD
	if err := json.Unmarshal(body, &jrd); err != nil {
		return "", err
	}
	actor := jrd.ActorURI()
	if actor == "" {
		return "", ErrNoActorAt
	}
	return actor, nil
}

func (c *Client) get(ctx context.Context, uri string, as Identity) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", ContentType+", "+LDType+", "+JRDType)

	if as.Key != nil {
		if err := signAs(as.Signing, req, as.KeyID, as.Key, nil); err != nil {
			return nil, err
		}
	}

	resp, err := c.fetcher.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, ErrRedirect
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("activitypub: %s answered %d", uri, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) Deliver(ctx context.Context, inbox string, document []byte, as Identity) (string, error) {
	scheme := as.Signing
	if scheme != Signing9421 {
		scheme = SigningCavage
	}

	status, err := c.deliverOnce(ctx, inbox, document, as, scheme)
	if err == nil {
		return scheme, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return scheme, err
	}

	knock := theOther(scheme)
	if _, second := c.deliverOnce(ctx, inbox, document, as, knock); second == nil {
		return knock, nil
	}
	return scheme, err
}

func (c *Client) deliverOnce(ctx context.Context, inbox string, document []byte,
	as Identity, scheme string) (int, error) {

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inbox, bytes.NewReader(document))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", ContentType)
	req.Header.Set("Accept", ContentType)

	if err := signAs(scheme, req, as.KeyID, as.Key, document); err != nil {
		return 0, err
	}

	resp, err := c.fetcher.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, ErrRedirect
	}
	return resp.StatusCode, fmt.Errorf("%w: %s answered %d", ErrRefused, inbox, resp.StatusCode)
}
