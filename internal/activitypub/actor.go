package activitypub

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const (
	ContentType = "application/activity+json"
	LDType      = `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`
	Public      = "https://www.w3.org/ns/activitystreams#Public"
)

var (
	ErrNotAnActor     = errors.New("activitypub: that document is not an actor we can use")
	ErrOriginMismatch = errors.New("activitypub: the activity and the actor come from different origins")
)

var defaultContext = []any{
	"https://www.w3.org/ns/activitystreams",
	"https://w3id.org/security/v1",
}

type Actor struct {
	Context           any        `json:"@context,omitempty"`
	ID                string     `json:"id"`
	Type              string     `json:"type"`
	PreferredUsername string     `json:"preferredUsername"`
	Name              string     `json:"name,omitempty"`
	Summary           string     `json:"summary,omitempty"`
	URL               string     `json:"url,omitempty"`
	Inbox             string     `json:"inbox"`
	Outbox            string     `json:"outbox,omitempty"`
	Followers         string     `json:"followers,omitempty"`
	Following         string     `json:"following,omitempty"`
	Endpoints         *Endpoints `json:"endpoints,omitempty"`
	PublicKey         *PublicKey `json:"publicKey,omitempty"`
	Discoverable      bool       `json:"discoverable,omitempty"`
	Published         string     `json:"published,omitempty"`
}

type Endpoints struct {
	SharedInbox string `json:"sharedInbox,omitempty"`
}

type PublicKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

type Activity struct {
	Context any             `json:"@context,omitempty"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Actor   string          `json:"actor"`
	To      []string        `json:"to,omitempty"`
	Cc      []string        `json:"cc,omitempty"`
	Object  json.RawMessage `json:"object,omitempty"`
}

type Note struct {
	Context      any      `json:"@context,omitempty"`
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	AttributedTo string   `json:"attributedTo"`
	Content      string   `json:"content"`
	Name         string   `json:"name,omitempty"`
	Source       *Source  `json:"source,omitempty"`
	InReplyTo    string   `json:"inReplyTo,omitempty"`
	Published    string   `json:"published,omitempty"`
	Updated      string   `json:"updated,omitempty"`
	URL          string   `json:"url,omitempty"`
	To           []string `json:"to,omitempty"`
	Cc           []string `json:"cc,omitempty"`
}

type Source struct {
	Content   string `json:"content"`
	MediaType string `json:"mediaType"`
}

func Context() any { return defaultContext }

func (a *Activity) ObjectID() string {
	if len(a.Object) == 0 {
		return ""
	}

	var asString string
	if err := json.Unmarshal(a.Object, &asString); err == nil {
		return asString
	}

	var nested struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(a.Object, &nested); err == nil {
		return nested.ID
	}
	return ""
}

func (a *Activity) Note() (*Note, error) {
	if len(a.Object) == 0 {
		return nil, errors.New("activitypub: the activity carries no object")
	}
	var note Note
	if err := json.Unmarshal(a.Object, &note); err != nil {
		return nil, err
	}
	if note.ID == "" {
		return nil, errors.New("activitypub: the object has no id")
	}
	return &note, nil
}

func SameOrigin(a, b string) bool {
	left, err := url.Parse(a)
	if err != nil || left.Host == "" {
		return false
	}
	right, err := url.Parse(b)
	if err != nil || right.Host == "" {
		return false
	}
	return strings.EqualFold(left.Host, right.Host) && left.Scheme == right.Scheme
}

func (a *Actor) usable() error {
	if a.ID == "" || a.Inbox == "" {
		return ErrNotAnActor
	}
	if a.PublicKey == nil || a.PublicKey.PublicKeyPEM == "" || a.PublicKey.ID == "" {
		return ErrNotAnActor
	}
	if !SameOrigin(a.ID, a.Inbox) || !SameOrigin(a.ID, a.PublicKey.ID) {
		return ErrOriginMismatch
	}
	if a.PublicKey.Owner != "" && a.PublicKey.Owner != a.ID {
		return ErrNotAnActor
	}
	return nil
}

func ParseActor(body []byte) (*Actor, error) {
	var actor Actor
	if err := json.Unmarshal(body, &actor); err != nil {
		return nil, err
	}
	if err := actor.usable(); err != nil {
		return nil, err
	}
	return &actor, nil
}

func (a *Actor) DeliveryInbox() string {
	if a.Endpoints != nil && a.Endpoints.SharedInbox != "" {
		return a.Endpoints.SharedInbox
	}
	return a.Inbox
}
