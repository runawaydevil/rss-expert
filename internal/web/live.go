package web

import (
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	livePollEvery  = 4 * time.Second
	liveHeartbeat  = 20 * time.Second
	liveMaxClients = 200
)

func (a *App) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not available here", http.StatusNotImplemented)
		return
	}
	if !a.live.admit() {
		http.Error(w, "too many open streams", http.StatusServiceUnavailable)
		return
	}
	defer a.live.leave()

	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")

	io.WriteString(w, "retry: 5000\n\n")
	flusher.Flush()

	poll := time.NewTicker(livePollEvery)
	defer poll.Stop()
	heartbeat := time.NewTicker(liveHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": still here\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			latest, err := a.sources.Newest(r.Context())
			if err != nil || latest <= since {
				continue
			}
			since = latest
			if _, err := io.WriteString(w,
				"event: fresh\ndata: "+strconv.FormatInt(latest, 10)+"\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type liveGate struct {
	slots chan struct{}
}

func newLiveGate(size int) *liveGate {
	return &liveGate{slots: make(chan struct{}, size)}
}

func (g *liveGate) admit() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *liveGate) leave() {
	<-g.slots
}
