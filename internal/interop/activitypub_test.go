package interop

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestTheOutboxCarriesPublishedCreateActivities(t *testing.T) {
	alice := newInstance(t, "alice.example")
	first := alice.publish("First", "The first post.", "")
	second := alice.publish("Second", "The second post.", "")

	outbox := alice.collection(t, alice.actorURI()+"/outbox")
	if total, _ := outbox["totalItems"].(float64); total != 2 {
		t.Fatalf("outbox totalItems = %v, want 2", outbox["totalItems"])
	}
	items, _ := outbox["orderedItems"].([]any)
	if len(items) != 2 {
		t.Fatalf("outbox carries %d activities, want 2", len(items))
	}

	latest, _ := items[0].(map[string]any)
	if latest["type"] != "Create" || latest["id"] != second.GUID+"#create" {
		t.Fatalf("latest outbox item is not the second Create: %v", latest)
	}
	object, _ := latest["object"].(map[string]any)
	if object["id"] != second.GUID || object["content"] == "" {
		t.Fatalf("outbox Create has no usable Note: %v", object)
	}
	if first.GUID == second.GUID {
		t.Fatal("the two test posts unexpectedly share an id")
	}
}

func TestAReplyFromActivityPubJoinsTheLocalThread(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	opening := bob.publish("Opening", "A post awaiting a remote answer.", "")

	document, err := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       alice.actorURI() + "/activities/reply-1",
		"type":     "Create",
		"actor":    alice.actorURI(),
		"to":       []string{bob.actorURI()},
		"object": map[string]any{
			"id":           alice.actorURI() + "/notes/reply-1",
			"type":         "Note",
			"attributedTo": alice.actorURI(),
			"content":      "<p>A reply delivered over ActivityPub.</p>",
			"inReplyTo":    opening.GUID,
			"published":    "2026-07-24T20:00:00Z",
			"url":          alice.actorURI() + "/notes/reply-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := alice.deliver(bob.inboxURL(), document,
		alice.actorURI()+"#main-key", alice.signingKey())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bob answered the ActivityPub reply with %d", resp.StatusCode)
	}

	replies, err := bob.sources.Replies(context.Background(), opening.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 {
		t.Fatalf("bob threaded %d ActivityPub replies, want 1", len(replies))
	}
	if replies[0].Key != alice.actorURI()+"/notes/reply-1" {
		t.Errorf("threaded reply key = %q", replies[0].Key)
	}
	if !strings.Contains(replies[0].HTML, "delivered over ActivityPub") {
		t.Errorf("threaded reply lost its content: %q", replies[0].HTML)
	}

	listed, err := bob.sources.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range listed {
		if source.FeedURL == alice.actorURI() {
			t.Fatal("answering a post put the actor in the list of feeds bob follows")
		}
	}

	page := bob.sourcesPage(t)
	if strings.Contains(page, alice.actorURI()) {
		t.Error("the sources page offers to refresh and remove a remote actor")
	}
}

func (i *instance) sourcesPage(t *testing.T) string {
	t.Helper()

	resp, err := http.Get(i.server.URL + "/sources")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
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

func TestUnlikingDoesNotUnfollow(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	alice.runDelivery()
	bob.runDelivery()

	post := bob.publish("Opening", "Something to like.", "")

	resp := alice.deliver(bob.inboxURL(),
		followDocument(alice.actorURI()+"/follows/1", alice.actorURI(), bob.actorURI()),
		alice.actorURI()+"#main-key", alice.signingKey())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the follow was answered %d", resp.StatusCode)
	}

	like, _ := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       alice.actorURI() + "/likes/1",
		"type":     "Like",
		"actor":    alice.actorURI(),
		"object":   post.GUID,
	})
	if resp := alice.deliver(bob.inboxURL(), like,
		alice.actorURI()+"#main-key", alice.signingKey()); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the like was answered %d", resp.StatusCode)
	}

	if counted := reactionsTo(t, bob, post.GUID); counted.Likes != 1 {
		t.Errorf("bob counted %d likes", counted.Likes)
	}

	undo, _ := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       alice.actorURI() + "/undos/1",
		"type":     "Undo",
		"actor":    alice.actorURI(),
		"object": map[string]any{
			"id":     alice.actorURI() + "/likes/1",
			"type":   "Like",
			"actor":  alice.actorURI(),
			"object": post.GUID,
		},
	})
	if resp := alice.deliver(bob.inboxURL(), undo,
		alice.actorURI()+"#main-key", alice.signingKey()); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the undo was answered %d", resp.StatusCode)
	}

	if counted := reactionsTo(t, bob, post.GUID); counted.Likes != 0 {
		t.Errorf("after the undo bob still counts %d likes", counted.Likes)
	}

	followers := bob.collection(t, bob.actorURI()+"/followers")
	if total, _ := followers["totalItems"].(float64); total != 1 {
		t.Fatalf("undoing a like cost bob a follower: %v remain", followers["totalItems"])
	}
}

func reactionsTo(t *testing.T, on *instance, guid string) activitypub.Reactions {
	t.Helper()
	counted, err := activitypub.New(on.db).ReactionsTo(context.Background(), guid)
	if err != nil {
		t.Fatal(err)
	}
	return counted
}

func TestAnEditReachesFollowersAsAnUpdate(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	alice.runDelivery()
	bob.runDelivery()

	if resp := alice.deliver(bob.inboxURL(),
		followDocument(alice.actorURI()+"/follows/1", alice.actorURI(), bob.actorURI()),
		alice.actorURI()+"#main-key", alice.signingKey()); resp.StatusCode != http.StatusAccepted {
		t.Fatal("the follow was refused")
	}

	post := bob.publish("Before", "The first wording.", "")
	bob.app.FanOut(context.Background(), post)
	waitFor(t, "the post to reach alice", func() bool { return settled(t, bob, post.GUID) })

	edited, err := bob.posts.Update(context.Background(), bob.owner, post.ID, "After", "The second wording.")
	if err != nil {
		t.Fatal(err)
	}
	bob.app.AnnounceEdit(context.Background(), edited)

	waitFor(t, "the edit to be delivered", func() bool {
		return deliveredType(t, bob, `"type":"Update"`)
	})
}

func TestAWithdrawalReachesFollowersAsADelete(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	alice.runDelivery()
	bob.runDelivery()

	if resp := alice.deliver(bob.inboxURL(),
		followDocument(alice.actorURI()+"/follows/1", alice.actorURI(), bob.actorURI()),
		alice.actorURI()+"#main-key", alice.signingKey()); resp.StatusCode != http.StatusAccepted {
		t.Fatal("the follow was refused")
	}

	post := bob.publish("Doomed", "This will be withdrawn.", "")
	withdrawn, err := bob.posts.Delete(context.Background(), bob.owner, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	bob.app.AnnounceWithdrawal(context.Background(), withdrawn)

	waitFor(t, "the withdrawal to be delivered", func() bool {
		return deliveredType(t, bob, `"type":"Delete"`)
	})
}

func deliveredType(t *testing.T, on *instance, needle string) bool {
	t.Helper()

	var found int
	err := on.db.Read.QueryRowContext(context.Background(),
		`select count(*) from job where kind = 'activitypub.deliver' and payload like ?`,
		"%"+needle+"%").Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	return found > 0
}

func remoteReply(t *testing.T, from *instance, to *instance, parentGUID, id string) []byte {
	t.Helper()

	document, err := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       from.actorURI() + "/activities/" + id,
		"type":     "Create",
		"actor":    from.actorURI(),
		"to":       []string{to.actorURI()},
		"object": map[string]any{
			"id":           from.actorURI() + "/notes/" + id,
			"type":         "Note",
			"attributedTo": from.actorURI(),
			"content":      "<p>An answer from the other side.</p>",
			"inReplyTo":    parentGUID,
			"published":    "2026-07-25T10:00:00Z",
			"url":          from.actorURI() + "/notes/" + id,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestAnswringARemoteReplyGoesBackOverActivityPub(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")
	alice.runDelivery()
	bob.runDelivery()

	opening := bob.publish("Opening", "A post that will be answered from abroad.", "")

	resp := alice.deliver(bob.inboxURL(),
		remoteReply(t, alice, bob, opening.GUID, "reply-1"),
		alice.actorURI()+"#main-key", alice.signingKey())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the inbound reply was answered %d", resp.StatusCode)
	}

	answer := bob.publish("", "And this is my answer.", alice.actorURI()+"/notes/reply-1")
	bob.app.ReplyAbroad(context.Background(), answer)

	var (
		inbox   string
		payload string
	)
	err := bob.db.Read.QueryRowContext(context.Background(),
		`select json_extract(payload, '$.inbox'), payload from job
		 where kind = 'activitypub.deliver' and json_extract(payload, '$.item_key') = ?`,
		answer.GUID).Scan(&inbox, &payload)
	if err != nil {
		t.Fatalf("answering a remote post queued nothing: %v", err)
	}

	if inbox != alice.inboxURL() {
		t.Errorf("the answer was addressed to %q, not to alice's inbox", inbox)
	}
	for _, want := range []string{
		`"type":"Create"`,
		`"inReplyTo":"` + alice.actorURI() + `/notes/reply-1"`,
		`"to":["` + alice.actorURI() + `"`,
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("the queued answer does not carry %s", want)
		}
	}
}

func TestTheSharedInboxRoutesToTheRightAccount(t *testing.T) {
	alice := newInstance(t, "alice.example")
	bob := newInstance(t, "bob.example")

	actor := bob.collection(t, bob.actorURI())
	endpoints, _ := actor["endpoints"].(map[string]any)
	shared, _ := endpoints["sharedInbox"].(string)
	if shared != bob.server.URL+"/inbox" {
		t.Fatalf("the actor advertises sharedInbox %q", shared)
	}

	resp := alice.deliver(shared,
		followDocument(alice.actorURI()+"/follows/1", alice.actorURI(), bob.actorURI()),
		alice.actorURI()+"#main-key", alice.signingKey())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the shared inbox answered %d", resp.StatusCode)
	}

	followers := bob.collection(t, bob.actorURI()+"/followers")
	if total, _ := followers["totalItems"].(float64); total != 1 {
		t.Errorf("a follow through the shared inbox left %v followers", followers["totalItems"])
	}

	following := bob.collection(t, bob.actorURI()+"/following")
	if following["type"] != "OrderedCollection" {
		t.Errorf("the following collection is not usable: %v", following)
	}
}

func TestDeliveryKnocksTwiceWhenTheFarSideWantsRFC9421(t *testing.T) {
	var (
		mu      sync.Mutex
		offered []string
	)

	modern := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Header.Get("Signature-Input") == "" {
			offered = append(offered, "cavage")
			http.Error(w, "this instance speaks RFC 9421 only", http.StatusUnauthorized)
			return
		}
		offered = append(offered, "rfc9421")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(modern.Close)

	bob := newInstance(t, "bob.example")
	key := bob.signingKey()
	client := activitypub.NewClient(activitypub.ClientOptions{ReachPrivate: true})

	worked, err := client.Deliver(context.Background(), modern.URL+"/inbox",
		[]byte(`{"type":"Create"}`),
		activitypub.Identity{
			KeyID:   bob.actorURI() + "#main-key",
			Key:     key,
			Signing: activitypub.SigningCavage,
		})
	if err != nil {
		t.Fatalf("the second knock did not land: %v", err)
	}
	if worked != activitypub.Signing9421 {
		t.Errorf("delivery reported %q as the scheme that worked", worked)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(offered) != 2 || offered[0] != "cavage" || offered[1] != "rfc9421" {
		t.Errorf("the knocks were %v; want cavage then rfc9421", offered)
	}
}

func TestTheSchemeThatWorkedIsRemembered(t *testing.T) {
	bob := newInstance(t, "bob.example")
	ap := activitypub.New(bob.db)
	ctx := context.Background()

	inbox := "https://modern.example/inbox"
	if got := ap.SigningFor(ctx, inbox); got != activitypub.SigningCavage {
		t.Errorf("an unknown host starts at %q, want the draft everyone speaks", got)
	}

	ap.RememberSigning(ctx, inbox, activitypub.Signing9421)
	if got := ap.SigningFor(ctx, inbox); got != activitypub.Signing9421 {
		t.Errorf("after learning, the host is signed with %q", got)
	}
	if got := ap.SigningFor(ctx, "https://other.example/inbox"); got != activitypub.SigningCavage {
		t.Errorf("the memory leaked to another host: %q", got)
	}
}
