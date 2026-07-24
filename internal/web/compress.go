package web

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

const compressAbove = 900

var writers = sync.Pool{
	New: func() any { return gzip.NewWriter(nil) },
}

func compressible(mediaType string) bool {
	mediaType, _, _ = strings.Cut(mediaType, ";")
	mediaType = strings.TrimSpace(strings.ToLower(mediaType))

	switch {
	case strings.HasPrefix(mediaType, "text/"),
		strings.HasSuffix(mediaType, "+xml"),
		strings.HasSuffix(mediaType, "+json"),
		strings.HasPrefix(mediaType, "application/json"),
		strings.HasPrefix(mediaType, "application/xml"),
		strings.HasPrefix(mediaType, "image/svg"):
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if name == "gzip" {
			return true
		}
	}
	return false
}

func compressed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}

		packer := &gzipWriter{ResponseWriter: w}
		defer packer.Close()
		next.ServeHTTP(packer, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	zip     *gzip.Writer
	decided bool
	plain   bool
	held    []byte
	status  int
}

func (g *gzipWriter) WriteHeader(status int) {
	g.status = status
	if status == http.StatusNotModified || status == http.StatusNoContent {
		g.plain, g.decided = true, true
	}
	if g.decided && g.plain {
		g.ResponseWriter.WriteHeader(status)
	}
}

func (g *gzipWriter) Write(body []byte) (int, error) {
	if !g.decided {
		g.held = append(g.held, body...)
		if len(g.held) < compressAbove {
			return len(body), nil
		}
		g.decide()
		return len(body), g.flushHeld()
	}
	if g.plain {
		return g.ResponseWriter.Write(body)
	}
	return g.zip.Write(body)
}

func (g *gzipWriter) decide() {
	g.decided = true

	h := g.Header()
	if h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) || len(g.held) < compressAbove {
		g.plain = true
		g.writeStatus()
		return
	}

	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	g.writeStatus()

	g.zip = writers.Get().(*gzip.Writer)
	g.zip.Reset(g.ResponseWriter)
}

func (g *gzipWriter) writeStatus() {
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.ResponseWriter.WriteHeader(g.status)
}

func (g *gzipWriter) flushHeld() error {
	held := g.held
	g.held = nil
	if g.plain {
		_, err := g.ResponseWriter.Write(held)
		return err
	}
	_, err := g.zip.Write(held)
	return err
}

func (g *gzipWriter) Close() {
	if !g.decided {
		g.decide()
		g.flushHeld()
	}
	if g.zip != nil {
		g.zip.Close()
		writers.Put(g.zip)
		g.zip = nil
	}
}
