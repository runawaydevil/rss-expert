package web

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/publish"
)

func (a *App) audience(post *publish.Post) ([]string, []string) {
	followers := a.actorURI(post.Handle) + "/followers"
	if post.Visibility == publish.VisibilityFollowers {
		return []string{followers}, nil
	}
	return []string{activitypub.Public}, []string{followers}
}

func (a *App) createActivity(post *publish.Post) map[string]any {
	to, cc := a.audience(post)
	return map[string]any{
		"@context": activitypub.Context(),
		"id":       post.GUID + "#create",
		"type":     "Create",
		"actor":    a.actorURI(post.Handle),
		"to":       to,
		"cc":       cc,
		"object":   a.note(post, post.Handle),
	}
}

func (a *App) updateActivity(post *publish.Post) map[string]any {
	stamp := post.Updated
	if stamp.IsZero() {
		stamp = post.Published
	}
	to, cc := a.audience(post)
	return map[string]any{
		"@context": activitypub.Context(),
		"id":       post.GUID + "#update-" + strconv.FormatInt(stamp.Unix(), 10),
		"type":     "Update",
		"actor":    a.actorURI(post.Handle),
		"to":       to,
		"cc":       cc,
		"object":   a.note(post, post.Handle),
	}
}

func (a *App) deleteActivity(post *publish.Post) map[string]any {
	to, cc := a.audience(post)
	return map[string]any{
		"@context": activitypub.Context(),
		"id":       post.GUID + "#delete",
		"type":     "Delete",
		"actor":    a.actorURI(post.Handle),
		"to":       to,
		"cc":       cc,
		"object": map[string]any{
			"id":         post.GUID,
			"type":       "Tombstone",
			"formerType": "Note",
			"deleted":    time.Now().UTC().Format(time.RFC3339),
		},
	}
}

func (a *App) profileActivity(actor *activitypub.Actor) map[string]any {
	return map[string]any{
		"@context": activitypub.Context(),
		"id":       actor.ID + "#update-profile",
		"type":     "Update",
		"actor":    actor.ID,
		"to":       []string{activitypub.Public},
		"cc":       []string{actor.Followers},
		"object":   actor,
	}
}

func (a *App) replyActivity(post *publish.Post, target string) map[string]any {
	note := a.note(post, post.Handle)
	note.To = append([]string{target}, note.To...)

	return map[string]any{
		"@context": activitypub.Context(),
		"id":       post.GUID + "#create",
		"type":     "Create",
		"actor":    a.actorURI(post.Handle),
		"to":       note.To,
		"cc":       note.Cc,
		"object":   note,
	}
}

func (a *App) broadcast(ctx context.Context, post *publish.Post, document map[string]any) {
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

	encoded, err := json.Marshal(document)
	if err != nil {
		a.log.Error("could not build an activity", "type", document["type"], "error", err)
		return
	}

	id, _ := document["id"].(string)
	for _, inbox := range inboxes {
		a.enqueueDelivery(ctx, deliverPayload{
			AccountID:  post.AccountID,
			Handle:     post.Handle,
			Inbox:      inbox,
			ItemKey:    post.GUID,
			ActivityID: id,
			Document:   encoded,
		})
	}
}

func (a *App) FanOut(ctx context.Context, post *publish.Post) {
	if !post.Federates() {
		return
	}
	a.broadcast(ctx, post, a.createActivity(post))
	a.ReplyAbroad(ctx, post)
}

func (a *App) AnnounceEdit(ctx context.Context, post *publish.Post) {
	if !post.Federates() {
		return
	}
	a.broadcast(ctx, post, a.updateActivity(post))
}

func (a *App) AnnounceWithdrawal(ctx context.Context, post *publish.Post) {
	if !post.Federates() {
		return
	}
	a.broadcast(ctx, post, a.deleteActivity(post))
}

func (a *App) ReplyAbroad(ctx context.Context, post *publish.Post) {
	if !a.federates || post.Handle == "" || post.InReplyTo == "" {
		return
	}

	actorURI, ok := a.sources.FederatedAuthor(ctx, post.InReplyTo)
	if !ok {
		return
	}

	actor, err := a.remoteActor(ctx, post.AccountID, actorURI)
	if err != nil {
		a.log.Error("could not reach the actor being answered",
			"actor", actorURI, "post", post.GUID, "error", err)
		return
	}

	activity := a.replyActivity(post, actor.ID)
	document, err := json.Marshal(activity)
	if err != nil {
		a.log.Error("could not build an outbound reply", "post", post.GUID, "error", err)
		return
	}

	id, _ := activity["id"].(string)
	a.enqueueDelivery(ctx, deliverPayload{
		AccountID:  post.AccountID,
		Handle:     post.Handle,
		Inbox:      actor.Inbox,
		ItemKey:    post.GUID,
		ActivityID: id,
		Document:   document,
	})
	a.log.Info("answering a post on another instance",
		"actor", actor.ID, "post", post.GUID, "in reply to", post.InReplyTo)
}

func (a *App) AnnounceProfile(ctx context.Context, accountID int64, handle string) {
	if !a.federates {
		return
	}

	actor, err := a.actorFor(ctx, handle)
	if err != nil {
		a.log.Error("could not build an actor to announce", "handle", handle, "error", err)
		return
	}

	activity := a.profileActivity(actor)
	document, err := json.Marshal(activity)
	if err != nil {
		return
	}

	inboxes, err := a.ap.Followers(ctx, accountID)
	if err != nil || len(inboxes) == 0 {
		return
	}
	id, _ := activity["id"].(string)
	for _, inbox := range inboxes {
		a.enqueueDelivery(ctx, deliverPayload{
			AccountID:  accountID,
			Handle:     handle,
			Inbox:      inbox,
			ItemKey:    actor.ID,
			ActivityID: id,
			Document:   document,
		})
	}
}
