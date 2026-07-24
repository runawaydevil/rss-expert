package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/ledger"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

const (
	kindDeliverActivity = "activitypub.deliver"
	fanOutCeiling       = 2000
)

type deliverPayload struct {
	AccountID int64           `json:"account_id"`
	Handle    string          `json:"handle"`
	Inbox     string          `json:"inbox"`
	ItemKey   string          `json:"item_key"`
	Document  json.RawMessage `json:"document"`
}

func (a *App) instanceIdentity(r *http.Request) activitypub.Identity {
	owner, err := a.accounts.Owner(r.Context())
	if err != nil || owner == nil {
		return activitypub.Identity{}
	}
	handle, err := a.posts.HandleFor(r.Context(), owner.ID)
	if err != nil || handle == "" {
		return activitypub.Identity{}
	}
	key, _, err := a.ap.EnsureKey(r.Context(), owner.ID)
	if err != nil {
		return activitypub.Identity{}
	}
	return activitypub.Identity{KeyID: a.keyID(handle), Key: key}
}

func fingerprint(parts ...string) string {
	sum := sha256.New()
	for _, part := range parts {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(sum.Sum(nil))[:22]
}

func (a *App) enqueueDelivery(ctx context.Context, payload deliverPayload) {
	_, err := a.queue.Enqueue(ctx, jobs.Spec{
		Kind:    kindDeliverActivity,
		Payload: payload,
		IdemKey: kindDeliverActivity + ":" + fingerprint(payload.Inbox, payload.ItemKey, string(payload.Document)),
	})
	if err != nil && !errors.Is(err, jobs.ErrDuplicate) {
		a.log.Error("could not queue an activity delivery", "error", err)
	}
}

func (a *App) queueAccept(r *http.Request, accountID int64, handle string,
	follow *activitypub.Activity, actor *activitypub.Actor) {

	uri := a.actorURI(handle)
	document, err := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       uri + "/accepts/" + fingerprint(follow.ID, actor.ID),
		"type":     "Accept",
		"actor":    uri,
		"object":   json.RawMessage(rawFollow(follow)),
	})
	if err != nil {
		a.log.Error("could not build an Accept", "error", err)
		return
	}

	a.enqueueDelivery(context.WithoutCancel(r.Context()), deliverPayload{
		AccountID: accountID,
		Handle:    handle,
		Inbox:     actor.Inbox,
		ItemKey:   follow.ID,
		Document:  document,
	})
}

func rawFollow(follow *activitypub.Activity) []byte {
	document, err := json.Marshal(map[string]any{
		"id":     follow.ID,
		"type":   "Follow",
		"actor":  follow.Actor,
		"object": follow.ObjectID(),
	})
	if err != nil {
		return []byte(`{"type":"Follow"}`)
	}
	return document
}

func (a *App) note(post *publish.Post, handle string) *activitypub.Note {
	uri := a.actorURI(handle)
	note := &activitypub.Note{
		Context:      activitypub.Context(),
		ID:           post.GUID,
		Type:         "Note",
		AttributedTo: uri,
		Content:      post.HTML,
		Name:         post.Title,
		InReplyTo:    post.InReplyTo,
		Published:    post.Published.UTC().Format(time.RFC3339),
		URL:          a.posts.PostURL(post.ID),
		To:           []string{activitypub.Public},
		Cc:           []string{uri + "/followers"},
	}
	if post.Edited() {
		note.Updated = post.Updated.UTC().Format(time.RFC3339)
	}
	return note
}

func (a *App) FanOut(ctx context.Context, post *publish.Post) {
	if !a.federates || post.Handle == "" {
		return
	}

	inboxes, err := a.ap.Followers(ctx, post.AccountID)
	if err != nil {
		a.log.Error("could not read followers", "error", err)
		return
	}
	if len(inboxes) == 0 {
		return
	}
	if len(inboxes) > fanOutCeiling {
		a.log.Warn("fan-out truncated",
			"followers", len(inboxes), "ceiling", fanOutCeiling, "post", post.GUID)
		inboxes = inboxes[:fanOutCeiling]
	}

	uri := a.actorURI(post.Handle)
	document, err := json.Marshal(map[string]any{
		"@context": activitypub.Context(),
		"id":       post.GUID + "#create",
		"type":     "Create",
		"actor":    uri,
		"to":       []string{activitypub.Public},
		"cc":       []string{uri + "/followers"},
		"object":   a.note(post, post.Handle),
	})
	if err != nil {
		a.log.Error("could not build a Create", "error", err)
		return
	}

	for _, inbox := range inboxes {
		a.enqueueDelivery(ctx, deliverPayload{
			AccountID: post.AccountID,
			Handle:    post.Handle,
			Inbox:     inbox,
			ItemKey:   post.GUID,
			Document:  document,
		})
	}
}

func (d *Deliverer) keyID(handle string) string {
	return d.base + "/users/" + handle + "#main-key"
}

func (d *Deliverer) deliverActivity(ctx context.Context, job *jobs.Job) error {
	var payload deliverPayload
	if err := job.Into(&payload); err != nil {
		return err
	}

	key, _, err := d.ap.EnsureKey(ctx, payload.AccountID)
	if err != nil {
		return err
	}

	started := time.Now()
	err = d.apClient.Deliver(ctx, payload.Inbox, payload.Document, activitypub.Identity{
		KeyID: d.keyID(payload.Handle),
		Key:   key,
	})

	attempt := ledger.Attempt{
		ItemKey:   payload.ItemKey,
		Target:    payload.Inbox,
		Protocol:  ledger.ActivityPub,
		AttemptNo: job.Attempts,
		Latency:   time.Since(started),
		Outcome:   ledger.OK,
	}
	if err != nil {
		attempt.Outcome = ledger.Failed
		if job.LastChance() {
			attempt.Outcome = ledger.GaveUp
		}
		if errors.Is(err, activitypub.ErrRefused) {
			attempt.ErrorKind = "refused"
		}
		attempt.ErrorDetail = err.Error()
	}

	if _, recordErr := d.ledger.Record(ctx, attempt); recordErr != nil {
		d.log.Error("could not record a delivery attempt", "error", recordErr)
	}
	return err
}
