package web

import (
	"context"

	"github.com/runawaydevil/rss-expert/internal/ingest"
	"github.com/runawaydevil/rss-expert/internal/reading"
)

const threadDepth = 6

type node struct {
	Post    postView
	Replies []node
	Deeper  int
}

func (a *App) thread(ctx context.Context, key string, depth int) []node {
	if depth >= threadDepth {
		return nil
	}

	items, err := a.sources.Replies(ctx, key)
	if err != nil {
		a.log.Error("could not read a branch of a thread", "key", key, "error", err)
		return nil
	}

	out := make([]node, 0, len(items))
	for i := range items {
		view := timelineViews(items[i : i+1])[0]
		view.Key = items[i].Key

		branch := node{Post: view}
		if depth+1 < threadDepth {
			branch.Replies = a.thread(ctx, items[i].Key, depth+1)
		} else {
			branch.Deeper = a.countReplies(ctx, items[i].Key)
		}
		out = append(out, branch)
	}
	return out
}

func (a *App) countReplies(ctx context.Context, key string) int {
	items, err := a.sources.Replies(ctx, key)
	if err != nil {
		return 0
	}
	return len(items)
}

func decorateThread(branches []node, flags map[string]reading.Flags) {
	for i := range branches {
		if f, ok := flags[branches[i].Post.Key]; ok {
			branches[i].Post.Read = f.Read
			branches[i].Post.Saved = f.Saved
		}
		decorateThread(branches[i].Replies, flags)
	}
}

func flatten(branches []node, keys []string) []string {
	for i := range branches {
		keys = append(keys, branches[i].Post.Key)
		keys = flatten(branches[i].Replies, keys)
	}
	return keys
}

var _ = ingest.Item{}
