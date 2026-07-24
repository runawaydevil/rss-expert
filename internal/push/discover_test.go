package push

import (
	"net/http"
	"net/url"
	"testing"
)

func at(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestLinkHeadersWinOverTheDocument(t *testing.T) {
	header := http.Header{}
	header.Add("Link", `<https://hub.example/>; rel="hub"`)
	header.Add("Link", `<https://site.example/feed.xml>; rel="self", <https://site.example/>; rel="alternate"`)

	body := []byte(`<rss><channel>
		<atom:link rel="hub" href="https://wrong.example/"/>
		<atom:link rel="self" href="https://wrong.example/feed.xml"/>
	</channel></rss>`)

	found := Find(at(t, "https://site.example/feed.xml"), header, body)
	if found.Hub != "https://hub.example/" {
		t.Errorf("hub = %q", found.Hub)
	}
	if found.Self != "https://site.example/feed.xml" {
		t.Errorf("self = %q", found.Self)
	}
}

func TestTheDocumentIsReadWhenThereAreNoHeaders(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
	<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel>
		<title>A site</title>
		<atom:link rel="hub" href="https://hub.example/endpoint"/>
		<atom:link rel="self" href="/feed.xml"/>
		<cloud domain="rpc.example.org" port="5337" path="/rsscloud/pleaseNotify"
		       registerProcedure="" protocol="http-post"/>
	</channel></rss>`)

	found := Find(at(t, "https://site.example/feed.xml"), nil, body)
	if found.Hub != "https://hub.example/endpoint" {
		t.Errorf("hub = %q", found.Hub)
	}
	if found.Self != "https://site.example/feed.xml" {
		t.Errorf("self = %q; a relative self must be resolved against the feed", found.Self)
	}
	if found.Cloud == nil {
		t.Fatal("the cloud element was not read")
	}
	if got := found.Cloud.Endpoint(); got != "http://rpc.example.org:5337/rsscloud/pleaseNotify" {
		t.Errorf("cloud endpoint = %q", got)
	}
}

func TestACloudWeCannotSpeakIsIgnored(t *testing.T) {
	body := []byte(`<rss><channel>
		<cloud domain="rpc.example.org" port="80" path="/RPC2"
		       registerProcedure="cloud.notify" protocol="xml-rpc"/>
	</channel></rss>`)

	if found := Find(at(t, "https://site.example/feed.xml"), nil, body); found.Cloud != nil {
		t.Errorf("an xml-rpc cloud was accepted: %+v", found.Cloud)
	}
}

func TestAFeedWithNoPushSaysNothing(t *testing.T) {
	found := Find(at(t, "https://site.example/feed.xml"), nil, []byte(`<rss><channel><title>x</title></channel></rss>`))
	if found.Hub != "" || found.Cloud != nil {
		t.Errorf("invented push endpoints: %+v", found)
	}
}

func TestSignaturesAreCheckedProperly(t *testing.T) {
	body := []byte("the delivered feed")
	secret := "a-shared-secret"

	if err := CheckSignature(Sign(secret, body), secret, body); err != nil {
		t.Errorf("our own signature did not verify: %v", err)
	}
	if err := CheckSignature(Sign(secret, body), "another-secret", body); err != ErrBadSecret {
		t.Errorf("a wrong secret gave %v", err)
	}
	if err := CheckSignature(Sign(secret, body), secret, []byte("tampered")); err != ErrBadSecret {
		t.Errorf("a tampered body gave %v", err)
	}
	if err := CheckSignature("", secret, body); err != ErrUnsigned {
		t.Errorf("an unsigned delivery gave %v", err)
	}
	if err := CheckSignature("md5=00", secret, body); err != ErrUnknownHash {
		t.Errorf("an unknown algorithm gave %v", err)
	}
	if err := CheckSignature("", "", body); err != nil {
		t.Errorf("no secret asked for, no signature needed, yet: %v", err)
	}
}
