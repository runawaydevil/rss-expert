package web

import (
	"net/http"
	"strings"
	"time"
)

const siteName = "rss-expert"

const defaultDescription = "A social reader for the open web: read feeds, publish feeds, and thread " +
	"conversations over plain RSS, keeping where every post came from."

func (a *App) seoDefaults(r *http.Request, data map[string]any) {
	setDefault(data, "SiteName", siteName)
	setDefault(data, "Description", defaultDescription)
	setDefault(data, "Canonical", a.absURL(r.URL.Path))
	setDefault(data, "OGType", "website")
	setDefault(data, "OGImage", a.absURL("/assets/mark.png"))
	if _, ok := data["NoIndex"]; !ok {
		data["NoIndex"] = !indexable(r.URL.Path)
	}
}

func setDefault(data map[string]any, key string, value any) {
	if _, ok := data[key]; !ok {
		data[key] = value
	}
}

func (a *App) absURL(path string) string {
	return a.posts.BaseURL() + path
}

func indexable(path string) bool {
	switch {
	case path == "/", path == "/rules":
		return true
	case strings.HasPrefix(path, "/users/"):
		return true
	case strings.HasPrefix(path, "/p/"):
		return !strings.HasSuffix(path, "/edit")
	}
	return false
}

func textExcerpt(htmlContent string, max int) string {
	var b strings.Builder
	inTag, lastSpace, truncated := false, false, false
	for _, r := range htmlContent {
		switch {
		case r == '<':
			inTag = true
			continue
		case r == '>':
			inTag = false
			continue
		case inTag:
			continue
		case r == '\n' || r == '\t' || r == '\r' || r == ' ':
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
		if len([]rune(b.String())) >= max {
			truncated = true
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if truncated {
		return out + "…"
	}
	return out
}

func (a *App) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte("User-agent: *\n" +
		"Allow: /\n" +
		"Disallow: /settings\n" +
		"Disallow: /admin\n" +
		"Disallow: /login\n" +
		"Disallow: /register\n" +
		"Disallow: /account\n" +
		"Disallow: /write\n" +
		"Sitemap: " + a.absURL("/sitemap.xml") + "\n"))
}

func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	writeURL := func(loc, lastmod string) {
		b.WriteString("<url><loc>")
		b.WriteString(xmlEscape(loc))
		b.WriteString("</loc>")
		if lastmod != "" {
			b.WriteString("<lastmod>" + lastmod + "</lastmod>")
		}
		b.WriteString("</url>\n")
	}

	writeURL(a.absURL("/"), "")
	writeURL(a.absURL("/rules"), "")

	if posts, err := a.posts.Recent(r.Context(), 500); err == nil {
		seenHandle := make(map[string]bool)
		for _, post := range posts {
			if post.Deleted {
				continue
			}
			writeURL(a.posts.PostURL(post.ID), lastmod(post.Published, post.Updated))
			if !seenHandle[post.Handle] {
				seenHandle[post.Handle] = true
				writeURL(a.absURL("/users/"+post.Handle), "")
			}
		}
	}

	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

func lastmod(published, updated time.Time) string {
	when := published
	if updated.After(published) {
		when = updated
	}
	if when.IsZero() {
		return ""
	}
	return when.UTC().Format("2006-01-02")
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}
