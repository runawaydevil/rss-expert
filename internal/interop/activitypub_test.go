package interop

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/ledger"
	"github.com/runawaydevil/rss-expert/internal/web"
)

func authorityOf(url string) string {
	return strings.TrimPrefix(url, "http://")
}

func (i *instance) actorURI() string {
	return i.server.URL + "/users/" + i.handle
}

func (i *instance) inboxURL() string {
	return i.actorURI() + "/inbox"
}

func (i *instance) signingKey() *rsa.PrivateKey {
	i.t.Helper()
	key, _, err := activitypub.New(i.db).EnsureKey(context.Background(), i.owner.ID)
	if err != nil {
		i.t.Fatal(err)
	}
	return key
}

func (i *instance) runDelivery() {
	i.t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	i.t.Cleanup(stop)
	go web.NewDeliverer(i.app).Run(ctx)
}

func (i *instance) deliver(inbox string, document []byte, keyID string, key *rsa.PrivateKey) *http.Response {
	i.t.Helper()

	req, err := http.NewRequest(http.MethodPost, inbox, bytes.NewReader(document))
	if err != nil {
		i.t.Fatal(err)
	}
	req.Header.Set("Content-Type", activitypub.ContentType)
	if err := activitypub.Sign(req, keyID, key, document); err != nil {
		i.t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		i.t.Fatal(err)
	}
	i.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func followDocument(id, from, to string) []byte {
	document, _ := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       id,
		"type":     "Follow",
		"actor":    from,
		"object":   to,
	})
	return document
}

func (i *instance) collection(t *testing.T, url string) map[string]any {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", activitypub.ContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d", url, resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestWebfingerFindsAnAccountAndTheActorFollows(t *testing.T) {
	alice := newInstance(t, "alice.example")

	finger := alice.collection(t,
		alice.server.URL+"/.well-known/webfinger?resource=acct:"+alice.handle+"@"+authorityOf(alice.server.URL))

	if finger["subject"] != "acct:"+alice.handle+"@"+authorityOf(alice.server.URL) {
		t.Errorf("subject = %v", finger["subject"])
	}

	links, _ := finger["links"].([]any)
	var actorURI string
	for _, raw := range links {
		link, _ := raw.(map[string]any)
		if link["rel"] == "self" {
			actorURI, _ = link["href"].(string)
		}
	}
	if actorURI != alice.actorURI() {
		t.Fatalf("webfinger points at %q, not %q", actorURI, alice.actorURI())
	}

	actor := alice.collection(t, actorURI)
	if actor["type"] != "Person" || actor["inbox"] != alice.inboxURL() {
		t.Fatalf("the actor document is not usable: %v", actor)
	}

	key, _ := actor["publicKey"].(map[string]any)
	if key["id"] != actorURI+"#main-key" || key["owner"] != actorURI {
		t.Errorf("the key does not belong to the actor: %v", key)
	}
	if pem, _ := key["publicKeyPem"].(string); len(pem) < 200 {
		t.Errorf("the public key looks empty: %q", pem)
	}
}

func TestTheProfilePageStillServesHTMLToABrowser(t *testing.T) {
	alice := newInstance(t, "alice.example")

	req, err := http.NewRequest(http.MethodGet, alice.actorURI(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("a browser was served %q", got)
	}
	if got := resp.Header.Get("Vary"); got != "Accept" {
		t.Errorf("Vary = %q; a shared cache would mix the two shapes", got)
	}
}

func TestSomebodyOnAnotherInstanceFollowsAndReceivesAPost(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	alice.runDelivery()
	bob.runDelivery()

	follow := alice.actorURI() + "/follows/1"
	resp := alice.deliver(bob.inboxURL(),
		followDocument(follow, alice.actorURI(), bob.actorURI()),
		alice.actorURI()+"#main-key", alice.signingKey())

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bob answered the follow with %d", resp.StatusCode)
	}

	followers := bob.collection(t, bob.actorURI()+"/followers")
	if total, _ := followers["totalItems"].(float64); total != 1 {
		t.Fatalf("bob counts %v followers after alice followed", followers["totalItems"])
	}

	waitFor(t, "bob's Accept to reach alice", func() bool {
		return settled(t, bob, follow)
	})

	post := bob.publish("hello", "a post that should reach alice", "")
	bob.app.FanOut(context.Background(), post)

	waitFor(t, "bob's post to reach alice's inbox", func() bool {
		return settled(t, bob, post.GUID)
	})
}

func settled(t *testing.T, on *instance, itemKey string) bool {
	t.Helper()

	attempts, err := ledger.New(on.db).Journey(context.Background(), itemKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.Protocol == ledger.ActivityPub && attempt.Outcome == ledger.OK {
			return true
		}
	}
	return false
}

func TestAForgedDeliveryIsRefused(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	mallory := newInstance(t, "mallory.example")

	document := followDocument(alice.actorURI()+"/follows/2", alice.actorURI(), bob.actorURI())

	for name, refused := range map[string]func() *http.Response{
		"signed with somebody else's key": func() *http.Response {
			return alice.deliver(bob.inboxURL(), document,
				alice.actorURI()+"#main-key", mallory.signingKey())
		},
		"a key that belongs to another host": func() *http.Response {
			return alice.deliver(bob.inboxURL(), document,
				mallory.actorURI()+"#main-key", mallory.signingKey())
		},
		"an activity id from another host": func() *http.Response {
			forged := followDocument(mallory.actorURI()+"/follows/3",
				alice.actorURI(), bob.actorURI())
			return alice.deliver(bob.inboxURL(), forged,
				alice.actorURI()+"#main-key", alice.signingKey())
		},
		"a body that changed after signing": func() *http.Response {
			tampered := followDocument(alice.actorURI()+"/follows/4",
				alice.actorURI(), bob.actorURI())

			req, err := http.NewRequest(http.MethodPost, bob.inboxURL(), bytes.NewReader(tampered))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", activitypub.ContentType)
			if err := activitypub.Sign(req,
				alice.actorURI()+"#main-key", alice.signingKey(), document); err != nil {
				t.Fatal(err)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { resp.Body.Close() })
			return resp
		},
		"no signature at all": func() *http.Response {
			resp, err := http.Post(bob.inboxURL(), activitypub.ContentType, bytes.NewReader(document))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { resp.Body.Close() })
			return resp
		},
	} {
		if got := refused().StatusCode; got != http.StatusUnauthorized {
			t.Errorf("%s was answered %d, not 401", name, got)
		}
	}

	followers := bob.collection(t, bob.actorURI()+"/followers")
	if total, _ := followers["totalItems"].(float64); total != 0 {
		t.Errorf("a forgery got through: bob counts %v followers", followers["totalItems"])
	}
}
