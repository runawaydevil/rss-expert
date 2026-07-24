package activitypub

import (
	"errors"
	"testing"
)

const genuine = `{
	"@context": ["https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"],
	"id": "https://mastodon.example/users/bob",
	"type": "Person",
	"preferredUsername": "bob",
	"inbox": "https://mastodon.example/users/bob/inbox",
	"endpoints": {"sharedInbox": "https://mastodon.example/inbox"},
	"publicKey": {
		"id": "https://mastodon.example/users/bob#main-key",
		"owner": "https://mastodon.example/users/bob",
		"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n"
	}
}`

func TestAGenuineActorIsAccepted(t *testing.T) {
	actor, err := ParseActor([]byte(genuine))
	if err != nil {
		t.Fatal(err)
	}
	if actor.PreferredUsername != "bob" {
		t.Errorf("username = %q", actor.PreferredUsername)
	}
	if got := actor.DeliveryInbox(); got != "https://mastodon.example/inbox" {
		t.Errorf("delivery inbox = %q; the shared inbox should win", got)
	}
}

func TestAnActorWhoseKeyLivesElsewhereIsRefused(t *testing.T) {
	forged := `{
		"id": "https://mastodon.example/users/bob",
		"type": "Person",
		"inbox": "https://mastodon.example/users/bob/inbox",
		"publicKey": {
			"id": "https://evil.example/keys/1",
			"owner": "https://mastodon.example/users/bob",
			"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n"
		}
	}`

	if _, err := ParseActor([]byte(forged)); !errors.Is(err, ErrOriginMismatch) {
		t.Errorf("an actor pointing at a key on another host gave %v", err)
	}
}

func TestAnActorWhoseInboxLivesElsewhereIsRefused(t *testing.T) {
	forged := `{
		"id": "https://mastodon.example/users/bob",
		"type": "Person",
		"inbox": "https://evil.example/collect",
		"publicKey": {
			"id": "https://mastodon.example/users/bob#main-key",
			"owner": "https://mastodon.example/users/bob",
			"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n"
		}
	}`

	if _, err := ParseActor([]byte(forged)); !errors.Is(err, ErrOriginMismatch) {
		t.Errorf("an actor whose inbox is on another host gave %v", err)
	}
}

func TestAKeyClaimingAnotherOwnerIsRefused(t *testing.T) {
	forged := `{
		"id": "https://mastodon.example/users/bob",
		"type": "Person",
		"inbox": "https://mastodon.example/users/bob/inbox",
		"publicKey": {
			"id": "https://mastodon.example/users/bob#main-key",
			"owner": "https://mastodon.example/users/alice",
			"publicKeyPem": "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n"
		}
	}`

	if _, err := ParseActor([]byte(forged)); !errors.Is(err, ErrNotAnActor) {
		t.Errorf("a key claiming a different owner gave %v", err)
	}
}

func TestAnActorWithoutAnInboxIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no inbox": `{"id":"https://m.example/users/b","type":"Person",
			"publicKey":{"id":"https://m.example/users/b#k","publicKeyPem":"x"}}`,
		"no key":   `{"id":"https://m.example/users/b","type":"Person","inbox":"https://m.example/i"}`,
		"no id":    `{"type":"Person","inbox":"https://m.example/i"}`,
		"nonsense": `not json at all`,
	} {
		if _, err := ParseActor([]byte(body)); err == nil {
			t.Errorf("%s was accepted as an actor", name)
		}
	}
}

func TestSameOriginIsStrict(t *testing.T) {
	same := [][2]string{
		{"https://m.example/users/b", "https://m.example/users/b/inbox"},
		{"https://M.Example/a", "https://m.example/b"},
	}
	for _, pair := range same {
		if !SameOrigin(pair[0], pair[1]) {
			t.Errorf("SameOrigin(%q, %q) = false", pair[0], pair[1])
		}
	}

	different := [][2]string{
		{"https://m.example/a", "https://evil.example/a"},
		{"https://m.example/a", "http://m.example/a"},
		{"https://m.example/a", "https://sub.m.example/a"},
		{"https://m.example/a", "not a url"},
		{"", "https://m.example/a"},
		{"https://m.example/a", "/relative"},
	}
	for _, pair := range different {
		if SameOrigin(pair[0], pair[1]) {
			t.Errorf("SameOrigin(%q, %q) = true", pair[0], pair[1])
		}
	}
}

func TestTheObjectIdIsReadInBothShapes(t *testing.T) {
	asString := &Activity{Object: []byte(`"https://m.example/p/1"`)}
	if got := asString.ObjectID(); got != "https://m.example/p/1" {
		t.Errorf("object as a bare string = %q", got)
	}

	nested := &Activity{Object: []byte(`{"id":"https://m.example/p/2","type":"Note"}`)}
	if got := nested.ObjectID(); got != "https://m.example/p/2" {
		t.Errorf("object as a document = %q", got)
	}

	if got := (&Activity{}).ObjectID(); got != "" {
		t.Errorf("an activity with no object gave %q", got)
	}
}
