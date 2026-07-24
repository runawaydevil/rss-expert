package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type limiter struct {
	mu      sync.Mutex
	seen    map[string]*bucket
	burst   float64
	perHour float64
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(burst, perHour float64) *limiter {
	return &limiter{
		seen:    make(map[string]*bucket),
		burst:   burst,
		perHour: perHour,
		swept:   time.Now(),
	}
}

func (l *limiter) allow(key string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.swept) > time.Hour {
		for k, b := range l.seen {
			if now.Sub(b.last) > time.Hour {
				delete(l.seen, k)
			}
		}
		l.swept = now
	}

	b, ok := l.seen[key]
	if !ok {
		l.seen[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

	refill := now.Sub(b.last).Hours() * l.perHour
	b.tokens = min(l.burst, b.tokens+refill)
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request, behindProxy bool) string {
	if behindProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			hops := strings.Split(forwarded, ",")
			if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
				return last
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
