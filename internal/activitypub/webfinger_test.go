package activitypub

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAnAcctAddressIsReadInEveryShapeMastodonSends(t *testing.T) {
	for _, resource := range []string{
		"acct:pablo@rss.expert",
		"acct:Pablo@RSS.Expert",
		"pablo@rss.expert",
		"@pablo@rss.expert",
		" acct:pablo@rss.expert ",
	} {
		address, err := ParseResource(resource)
		if err != nil {
			t.Fatalf("%q: %v", resource, err)
		}
		if address.User != "pablo" || address.Host != "rss.expert" {
			t.Errorf("%q parsed as %q@%q", resource, address.User, address.Host)
		}
	}
}

func TestAnythingThatIsNotAnAccountIsRefused(t *testing.T) {
	for _, resource := range []string{
		"",
		"pablo",
		"@rss.expert",
		"pablo@",
		"https://rss.expert/users/pablo",
		"mailto:pablo@rss.expert",
	} {
		if _, err := ParseResource(resource); !errors.Is(err, ErrNotAnAccount) {
			t.Errorf("%q was accepted as an account address", resource)
		}
	}
}

func TestTheDescriptorPointsAtTheActorAndThePage(t *testing.T) {
	address := Address{User: "pablo", Host: "rss.expert"}
	actor := "https://rss.expert/users/pablo"

	encoded, err := json.Marshal(Descriptor(address, actor, actor))
	if err != nil {
		t.Fatal(err)
	}

	var back JRD
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if back.Subject != "acct:pablo@rss.expert" {
		t.Errorf("subject = %q", back.Subject)
	}
	if got := back.ActorURI(); got != actor {
		t.Errorf("the descriptor points at %q", got)
	}
}

func TestADescriptorWithoutAnActorLinkGivesNothing(t *testing.T) {
	jrd := JRD{Links: []JRDLink{
		{Rel: relProfil, Type: "text/html", Href: "https://rss.expert/users/pablo"},
		{Rel: relSelf, Type: "text/html", Href: "https://rss.expert/users/pablo"},
	}}
	if got := jrd.ActorURI(); got != "" {
		t.Errorf("an html self link was taken for an actor: %q", got)
	}
}

func TestTheFingerURLEscapesTheResource(t *testing.T) {
	got := FingerURL("rss.expert", "acct:pablo@rss.expert")
	want := "https://rss.expert/.well-known/webfinger?resource=acct%3Apablo%40rss.expert"
	if got != want {
		t.Errorf("FingerURL = %q", got)
	}
}
