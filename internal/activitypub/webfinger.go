package activitypub

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	JRDType   = "application/jrd+json"
	relSelf   = "self"
	relProfil = "http://webfinger.net/rel/profile-page"
)

var ErrNotAnAccount = errors.New("activitypub: that resource is not an acct: address")

type JRD struct {
	Subject string    `json:"subject"`
	Aliases []string  `json:"aliases,omitempty"`
	Links   []JRDLink `json:"links,omitempty"`
}

type JRDLink struct {
	Rel      string `json:"rel"`
	Type     string `json:"type,omitempty"`
	Href     string `json:"href,omitempty"`
	Template string `json:"template,omitempty"`
}

type Address struct {
	User string
	Host string
}

func (a Address) String() string { return "acct:" + a.User + "@" + a.Host }

func ParseResource(resource string) (Address, error) {
	trimmed := strings.TrimSpace(resource)
	trimmed = strings.TrimPrefix(trimmed, "acct:")

	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		return Address{}, ErrNotAnAccount
	}

	user, host, ok := strings.Cut(strings.TrimPrefix(trimmed, "@"), "@")
	if !ok || user == "" || host == "" {
		return Address{}, ErrNotAnAccount
	}
	return Address{User: strings.ToLower(user), Host: strings.ToLower(host)}, nil
}

func Descriptor(address Address, actorURI, profileURI string) JRD {
	return JRD{
		Subject: address.String(),
		Aliases: []string{profileURI, actorURI},
		Links: []JRDLink{
			{Rel: relProfil, Type: "text/html", Href: profileURI},
			{Rel: relSelf, Type: ContentType, Href: actorURI},
		},
	}
}

func (j *JRD) ActorURI() string {
	for _, link := range j.Links {
		if link.Rel != relSelf {
			continue
		}
		if link.Type == ContentType || strings.HasPrefix(link.Type, "application/ld+json") {
			return link.Href
		}
	}
	return ""
}

func FingerURL(host, resource string) string {
	return fmt.Sprintf("https://%s/.well-known/webfinger?resource=%s", host, url.QueryEscape(resource))
}
