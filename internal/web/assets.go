package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed assets
var assetFiles embed.FS

type asset struct {
	body      []byte
	gzipped   []byte
	mediaType string
	version   string
	etag      string
}

var (
	assets    = map[string]*asset{}
	templates *template.Template
	builtAt   = time.Now().UTC()
)

func init() {
	sub, err := fs.Sub(assetFiles, "assets")
	if err != nil {
		panic(err)
	}

	err = fs.WalkDir(sub, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.HasSuffix(name, ".html") {
			return err
		}
		body, err := fs.ReadFile(sub, name)
		if err != nil {
			return err
		}
		assets[name] = newAsset(body, name)
		return nil
	})
	if err != nil {
		panic(err)
	}

	for name, a := range assets {
		if strings.HasSuffix(name, ".css") {
			assets[name] = newAsset(versionedURLs(a.body), name)
		}
	}

	templates = template.Must(template.New("").Funcs(template.FuncMap{
		"asset": assetURL,
		"icon":  icon,
	}).ParseFS(assetFiles, "assets/*.html"))
}

func newAsset(body []byte, name string) *asset {
	sum := sha256.Sum256(body)
	version := hex.EncodeToString(sum[:])[:10]

	mediaType := mime.TypeByExtension(path.Ext(name))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	a := &asset{body: body, mediaType: mediaType, version: version, etag: `"` + version + `"`}
	if compressible(mediaType) {
		if packed, ok := squeeze(body); ok {
			a.gzipped = packed
		}
	}
	return a
}

func versionedURLs(css []byte) []byte {
	for name, a := range assets {
		css = bytes.ReplaceAll(css,
			[]byte(`"/assets/`+name+`"`),
			[]byte(`"/assets/`+name+`?v=`+a.version+`"`))
	}
	return css
}

func squeeze(body []byte) ([]byte, bool) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, false
	}
	if _, err := w.Write(body); err != nil {
		return nil, false
	}
	if err := w.Close(); err != nil {
		return nil, false
	}
	if buf.Len() >= len(body) {
		return nil, false
	}
	return buf.Bytes(), true
}

func icon(name string) template.HTML {
	a, ok := assets["icons/"+name+".svg"]
	if !ok {
		return ""
	}
	if !bytes.HasPrefix(a.body, []byte("<svg")) {
		return ""
	}
	return template.HTML(`<svg class="icon" aria-hidden="true" focusable="false"` + string(a.body[4:]))
}

func assetURL(name string) string {
	a, ok := assets[name]
	if !ok {
		return "/assets/" + name
	}
	return "/assets/" + name + "?v=" + a.version
}

func assetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/assets/")

		a, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}

		h := w.Header()
		h.Set("Content-Type", a.mediaType)
		h.Set("ETag", a.etag)
		if r.URL.Query().Get("v") == a.version {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}

		if strings.Contains(r.Header.Get("If-None-Match"), a.version) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		body := a.body
		if a.gzipped != nil && acceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			h.Set("Vary", "Accept-Encoding")
			body = a.gzipped
		}

		http.ServeContent(w, r, name, builtAt, bytes.NewReader(body))
	})
}
