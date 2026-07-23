package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assetFiles embed.FS

var (
	assetFS      fs.FS
	assetVersion = map[string]string{}
	templates    *template.Template
)

func init() {
	sub, err := fs.Sub(assetFiles, "assets")
	if err != nil {
		panic(err)
	}
	assetFS = sub

	err = fs.WalkDir(sub, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := fs.ReadFile(sub, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		assetVersion[name] = hex.EncodeToString(sum[:])[:10]
		return nil
	})
	if err != nil {
		panic(err)
	}

	templates = template.Must(template.New("").Funcs(template.FuncMap{
		"asset": assetURL,
		"icon":  icon,
	}).ParseFS(assetFiles, "assets/*.html"))
}

func icon(name string) template.HTML {
	body, err := fs.ReadFile(assetFS, "icons/"+name+".svg")
	if err != nil {
		return ""
	}
	open := strings.Index(string(body), ">")
	if open < 0 {
		return ""
	}
	return template.HTML(`<svg class="icon" aria-hidden="true" focusable="false"` + string(body)[4:])
}

func assetURL(name string) string {
	version, ok := assetVersion[name]
	if !ok {
		return "/assets/" + name
	}
	return "/assets/" + name + "?v=" + version
}

func assetHandler() http.Handler {
	return http.StripPrefix("/assets/", cacheByVersion(http.FileServerFS(assetFS)))
}

func cacheByVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
		if v := r.URL.Query().Get("v"); v != "" && v == assetVersion[name] {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
