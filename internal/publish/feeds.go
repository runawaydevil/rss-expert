package publish

import (
	"context"
	"time"

	"github.com/runawaydevil/rss-expert/internal/feed"
	"github.com/runawaydevil/rss-expert/internal/feedout"
)

const FeedItemLimit = 100

func (s *Store) emitOptions(now time.Time) feedout.RSSOptions {
	return feedout.RSSOptions{
		Generator: "rss-expert",
		Docs:      "https://www.rssboard.org/rss-specification",
		BuildTime: now,
	}
}

func (s *Store) item(post *Post) feed.Item {
	item := feed.Item{
		GUID:            post.GUID,
		GUIDIsPermalink: true,
		Link:            post.GUID,
		Title:           post.Title,
		HTML:            post.HTML,
		Markdown:        post.Markdown,
		Published:       post.Published,
		Updated:         post.Updated,
		InReplyTo:       post.InReplyTo,
		Source: &feed.Source{
			URL:  s.AccountFeedURL(post.Handle),
			Name: post.Handle,
		},
	}
	for _, file := range post.Media {
		item.Enclosures = append(item.Enclosures, feed.Enclosure{
			URL:    s.baseURL() + file.URL(),
			Type:   file.MediaType,
			Length: file.Bytes,
		})
	}
	if post.ReplyCount > 0 {
		item.Comments = &feed.Comments{
			Count:   post.ReplyCount,
			FeedURL: s.RepliesURL(post.ID),
		}
	}
	return item
}

func (s *Store) items(posts []*Post) []feed.Item {
	out := make([]feed.Item, 0, len(posts))
	for _, post := range posts {
		out = append(out, s.item(post))
	}
	return out
}

func (s *Store) AccountFeed(ctx context.Context, handle string) ([]byte, error) {
	posts, err := s.ByHandle(ctx, handle, FeedItemLimit)
	if err != nil {
		return nil, err
	}

	f := &feed.Feed{
		Title:       handle + " on " + s.domain,
		Link:        s.baseURL() + "/users/" + handle,
		Description: "Posts by " + handle,
		Language:    "en-us",
		Self:        s.AccountFeedURL(handle),
		Items:       s.items(posts),
	}
	if len(posts) > 0 {
		f.Updated = posts[0].Published
	}
	return feedout.RSS(f, s.emitOptions(time.Now().UTC())), nil
}

func (s *Store) Firehose(ctx context.Context) ([]byte, error) {
	posts, err := s.Recent(ctx, FeedItemLimit)
	if err != nil {
		return nil, err
	}

	f := &feed.Feed{
		Title:       "All posts on " + s.domain,
		Link:        s.baseURL() + "/",
		Description: "Every post from every account on this instance",
		Language:    "en-us",
		Self:        s.FirehoseURL(),
		Items:       s.items(posts),
	}
	if len(posts) > 0 {
		f.Updated = posts[0].Published
	}
	return feedout.RSS(f, s.emitOptions(time.Now().UTC())), nil
}

func (s *Store) RepliesFeed(ctx context.Context, id int64) ([]byte, error) {
	parent, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	replies, err := s.Replies(ctx, parent.GUID, FeedItemLimit)
	if err != nil {
		return nil, err
	}

	title := "Replies to " + parent.Handle
	if parent.Title != "" {
		title = "Replies to " + parent.Title
	}

	f := &feed.Feed{
		Title:       title,
		Link:        parent.GUID,
		Description: title,
		Language:    "en-us",
		Self:        s.RepliesURL(parent.ID),
		Items:       s.items(replies),
	}
	if len(replies) > 0 {
		f.Updated = replies[len(replies)-1].Published
	}
	return feedout.RSS(f, s.emitOptions(time.Now().UTC())), nil
}
