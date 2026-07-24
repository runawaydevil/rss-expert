package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/runawaydevil/rss-expert/internal/activitypub"
	"github.com/runawaydevil/rss-expert/internal/jobs"
	"github.com/runawaydevil/rss-expert/internal/ledger"
	"github.com/runawaydevil/rss-expert/internal/publish"
	"github.com/runawaydevil/rss-expert/internal/webmention"
)

const (
	kindVerifyMention = "webmention.verify"
	kindSendMention   = "webmention.send"
	deliveryWorkers   = 2
	deliveryIdle      = 15 * time.Second
)

var errDuplicateJob = jobs.ErrDuplicate

type verifyPayload struct {
	MentionID int64 `json:"mention_id"`
}

type sendPayload struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func queueSpec(mentionID int64) jobs.Spec {
	return jobs.Spec{
		Kind:    kindVerifyMention,
		Payload: verifyPayload{MentionID: mentionID},
		IdemKey: kindVerifyMention + ":" + strconv.FormatInt(mentionID, 10),
	}
}

func (a *App) afterPublish(r *http.Request, post *publish.Post) {
	a.announce(context.WithoutCancel(r.Context()), post)
	a.FanOut(context.WithoutCancel(r.Context()), post)

	if post.InReplyTo == "" {
		return
	}
	_, err := a.queue.Enqueue(r.Context(), jobs.Spec{
		Kind:    kindSendMention,
		Payload: sendPayload{Source: post.GUID, Target: post.InReplyTo},
		IdemKey: kindSendMention + ":" + post.GUID + ":" + post.InReplyTo,
	})
	if err != nil && !errors.Is(err, jobs.ErrDuplicate) {
		a.log.Error("could not queue a webmention", "error", err)
	}
}

func (a *App) announce(ctx context.Context, post *publish.Post) {
	topics := []string{a.posts.FirehoseURL()}
	if post.Handle != "" {
		topics = append(topics, a.posts.AccountFeedURL(post.Handle))
	}
	if post.InReplyTo != "" {
		topics = append(topics, a.posts.RepliesURL(post.ID))
	}

	for _, topic := range topics {
		go a.Distribute(ctx, topic)
	}
}

type Deliverer struct {
	ap       *activitypub.Store
	apClient *activitypub.Client
	base     string
	queue    *jobs.Queue
	mentions *webmention.Store
	ledger   *ledger.Ledger
	log      *slog.Logger
}

func NewDeliverer(app *App) *Deliverer {
	return &Deliverer{
		ap:       app.ap,
		apClient: app.apClient,
		base:     app.posts.BaseURL(),
		queue:    app.queue,
		mentions: app.mentions,
		ledger:   app.ledger,
		log:      app.log,
	}
}

func (d *Deliverer) Run(ctx context.Context) {
	d.log.Info("delivery worker started", "workers", deliveryWorkers)

	for {
		worked, err := d.once(ctx)
		if err != nil && ctx.Err() == nil {
			d.log.Error("delivery sweep failed", "error", err)
		}
		if worked {
			continue
		}

		select {
		case <-ctx.Done():
			d.log.Info("delivery worker stopped")
			return
		case <-d.queue.Waiting():
		case <-time.After(deliveryIdle):
		}
	}
}

func (d *Deliverer) once(ctx context.Context) (bool, error) {
	job, err := d.queue.Claim(ctx, kindVerifyMention, kindSendMention, kindDeliverActivity)
	if errors.Is(err, jobs.ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	switch job.Kind {
	case kindVerifyMention:
		err = d.verify(ctx, job)
	case kindSendMention:
		err = d.send(ctx, job)
	case kindDeliverActivity:
		err = d.deliverActivity(ctx, job)
	}

	if err != nil {
		d.log.Warn("delivery job failed", "kind", job.Kind, "attempt", job.Attempts, "error", err)
		return true, d.queue.Fail(ctx, job, err)
	}
	return true, d.queue.Done(ctx, job.ID)
}

func (d *Deliverer) verify(ctx context.Context, job *jobs.Job) error {
	var payload verifyPayload
	if err := job.Into(&payload); err != nil {
		return err
	}

	mention, err := d.mentions.ByID(ctx, payload.MentionID)
	if err != nil {
		return err
	}
	if mention.Settled() {
		return nil
	}
	return d.mentions.VerifyIncoming(ctx, mention)
}

func (d *Deliverer) send(ctx context.Context, job *jobs.Job) error {
	var payload sendPayload
	if err := job.Into(&payload); err != nil {
		return err
	}

	started := time.Now()
	mention, err := d.mentions.Send(ctx, payload.Source, payload.Target)

	attempt := ledger.Attempt{
		ItemKey:   payload.Source,
		Target:    payload.Target,
		Protocol:  ledger.Webmention,
		AttemptNo: job.Attempts,
		Latency:   time.Since(started),
		Outcome:   ledger.OK,
	}
	if mention != nil && mention.Endpoint != "" {
		attempt.Target = mention.Endpoint
	}
	if err != nil {
		attempt.Outcome = ledger.Failed
		if job.LastChance() {
			attempt.Outcome = ledger.GaveUp
		}
		if errors.Is(err, webmention.ErrNoEndpoint) {
			attempt.Outcome = ledger.Rejected
			attempt.ErrorKind = "no endpoint"
		}
		attempt.ErrorDetail = err.Error()
	}

	if _, recordErr := d.ledger.Record(ctx, attempt); recordErr != nil {
		d.log.Error("could not record a delivery attempt", "error", recordErr)
	}

	if attempt.Outcome == ledger.Rejected {
		return nil
	}
	return err
}
