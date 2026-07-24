package push

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Endpoints struct {
	Hub   string
	Self  string
	Cloud *Cloud
}

type Cloud struct {
	Domain   string
	Port     int
	Path     string
	Protocol string
}

func (c *Cloud) Endpoint() string {
	scheme := "http"
	if c.Port == 443 {
		scheme = "https"
	}
	host := c.Domain
	if c.Port != 0 && c.Port != 80 && c.Port != 443 {
		host += ":" + strconv.Itoa(c.Port)
	}
	path := c.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return scheme + "://" + host + path
}

func (c *Cloud) usable() bool {
	return c != nil && c.Domain != "" && c.Path != "" && strings.EqualFold(c.Protocol, "http-post")
}

func Find(base *url.URL, header http.Header, body []byte) Endpoints {
	found := fromLinkHeaders(header)

	inFeed := fromFeed(body)
	if found.Hub == "" {
		found.Hub = inFeed.Hub
	}
	if found.Self == "" {
		found.Self = inFeed.Self
	}
	if !found.Cloud.usable() {
		found.Cloud = inFeed.Cloud
	}
	if !found.Cloud.usable() {
		found.Cloud = nil
	}

	found.Hub = absolute(base, found.Hub)
	found.Self = absolute(base, found.Self)
	return found
}

func absolute(base *url.URL, raw string) string {
	if raw == "" || base == nil {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(parsed)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	return resolved.String()
}

func fromLinkHeaders(header http.Header) Endpoints {
	var found Endpoints
	for _, value := range header.Values("Link") {
		for _, link := range splitLinks(value) {
			target, rel := parseLink(link)
			if target == "" {
				continue
			}
			switch {
			case rel == "hub" && found.Hub == "":
				found.Hub = target
			case rel == "self" && found.Self == "":
				found.Self = target
			}
		}
	}
	return found
}

func splitLinks(value string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range value {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, value[start:i])
				start = i + 1
			}
		}
	}
	return append(out, value[start:])
}

func parseLink(link string) (target, rel string) {
	parts := strings.Split(strings.TrimSpace(link), ";")
	if len(parts) == 0 {
		return "", ""
	}

	target = strings.TrimSpace(parts[0])
	if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
		return "", ""
	}
	target = target[1 : len(target)-1]

	for _, part := range parts[1:] {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "rel") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		for _, candidate := range strings.Fields(strings.ToLower(value)) {
			if candidate == "hub" || candidate == "self" {
				return target, candidate
			}
		}
	}
	return target, ""
}

func fromFeed(body []byte) Endpoints {
	var found Endpoints
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	decoder.Strict = false

	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 4 {
				continue
			}
			switch strings.ToLower(element.Name.Local) {
			case "link":
				rel, href := "", ""
				for _, attr := range element.Attr {
					switch strings.ToLower(attr.Name.Local) {
					case "rel":
						rel = strings.ToLower(attr.Value)
					case "href":
						href = attr.Value
					}
				}
				if href == "" {
					continue
				}
				if rel == "hub" && found.Hub == "" {
					found.Hub = href
				}
				if rel == "self" && found.Self == "" {
					found.Self = href
				}
			case "cloud":
				cloud := &Cloud{}
				for _, attr := range element.Attr {
					switch strings.ToLower(attr.Name.Local) {
					case "domain":
						cloud.Domain = attr.Value
					case "port":
						cloud.Port, _ = strconv.Atoi(attr.Value)
					case "path":
						cloud.Path = attr.Value
					case "protocol":
						cloud.Protocol = attr.Value
					}
				}
				if cloud.usable() && found.Cloud == nil {
					found.Cloud = cloud
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return found
}
