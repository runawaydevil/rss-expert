package web

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	AccountID  int64           `json:"account_id"`
	Handle     string          `json:"handle"`
	Inbox      string          `json:"inbox"`
	ItemKey    string          `json:"item_key"`
	ActivityID string          `json:"-"`
	Document   json.RawMessage `json:"document"`
}

func (a *App) instanceIdentity(ctx context.Context) activitypub.Identity {
	owner, err := a.accounts.Owner(ctx)
	if err != nil || owner == nil {
		return activitypub.Identity{}
	}
	handle, err := a.posts.HandleFor(ctx, owner.ID)
	if err != nil || handle == "" {
		return activitypub.Identity{}
	}
	key, _, err := a.ap.EnsureKey(ctx, owner.ID)
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
	tag := payload.ActivityID
	if tag == "" {
		tag = fingerprint(string(payload.Document))
	}
	_, err := a.queue.Enqueue(ctx, jobs.Spec{
		Kind:    kindDeliverActivity,
		Payload: payload,
		IdemKey: kindDeliverActivity + ":" + fingerprint(payload.Inbox, tag),
	})
	if err != nil && !errors.Is(err, jobs.ErrDuplicate) {
		a.log.Error("could not queue an activity delivery", "error", err)
	}
}

func (a *App) queueAccept(ctx context.Context, accountID int64, handle string,
	follow *activitypub.Activity, actor *activitypub.Actor) {

	uri := a.actorURI(handle)
	acceptID := uri + "/accepts/" + fingerprint(follow.ID, actor.ID)
	document, err := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       acceptID,
		"type":     "Accept",
		"actor":    uri,
		"object":   json.RawMessage(rawFollow(follow)),
	})
	if err != nil {
		a.log.Error("could not build an Accept", "error", err)
		return
	}

	a.enqueueDelivery(ctx, deliverPayload{
		AccountID:  accountID,
		Handle:     handle,
		Inbox:      actor.Inbox,
		ItemKey:    follow.ID,
		ActivityID: acceptID,
		Document:   document,
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

func (d *Deliverer) keyID(handle string) string {
	return d.base + "/users/" + handle + "#main-key"
}

func (d *Deliverer) signingKey(ctx context.Context, accountID int64) (*rsa.PrivateKey, error) {
	if cached, ok := d.keys.Load(accountID); ok {
		return cached.(*rsa.PrivateKey), nil
	}
	key, _, err := d.ap.EnsureKey(ctx, accountID)
	if err != nil {
		return nil, err
	}
	d.keys.Store(accountID, key)
	return key, nil
}

func (d *Deliverer) deliverActivity(ctx context.Context, job *jobs.Job) error {
	var payload deliverPayload
	if err := job.Into(&payload); err != nil {
		return err
	}

	key, err := d.signingKey(ctx, payload.AccountID)
	if err != nil {
		return err
	}

	started := time.Now()
	preferred := d.ap.SigningFor(ctx, payload.Inbox)
	worked, err := d.apClient.Deliver(ctx, payload.Inbox, payload.Document, activitypub.Identity{
		KeyID:   d.keyID(payload.Handle),
		Key:     key,
		Signing: preferred,
	})
	if err == nil && worked != preferred {
		d.ap.RememberSigning(ctx, payload.Inbox, worked)
		d.log.Info("the far side wants a different signature",
			"inbox", payload.Inbox, "signing", worked)
	}

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
