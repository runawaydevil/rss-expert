package mail

import (
	"context"
	"net/smtp"
	"strings"
	"testing"
)

func captured(t *testing.T, rawURL string) (*Sender, *string, *[]string) {
	t.Helper()

	sender, err := New(rawURL, "instance@example.org")
	if err != nil {
		t.Fatal(err)
	}

	var body string
	var to []string
	sender.send = func(addr string, a smtp.Auth, from string, recipients []string, msg []byte) error {
		body = string(msg)
		to = recipients
		return nil
	}
	return sender, &body, &to
}

func TestAMessageLooksLikeMail(t *testing.T) {
	sender, body, to := captured(t, "smtp://user:secret@mail.example.org:587")

	err := sender.Send(context.Background(), Message{
		To:      "reader@example.org",
		Subject: "Your sign-in link",
		Body:    "One line.\nAnother line.",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(*to) != 1 || (*to)[0] != "reader@example.org" {
		t.Errorf("recipients = %v", *to)
	}
	for _, want := range []string{
		"From: instance@example.org",
		"To: reader@example.org",
		"Subject: Your sign-in link",
		"Content-Type: text/plain; charset=utf-8",
		"Auto-Submitted: auto-generated",
		"One line.\r\nAnother line.",
	} {
		if !strings.Contains(*body, want) {
			t.Errorf("the message is missing %q:\n%s", want, *body)
		}
	}
	if !strings.Contains(*body, "\r\n\r\n") {
		t.Error("there is no blank line between the headers and the body")
	}
}

func TestTheFromAddressCanBeSetOnTheURL(t *testing.T) {
	sender, body, _ := captured(t, "smtps://mail.example.org?from=hello%40example.org")

	if sender.From() != "hello@example.org" {
		t.Errorf("from = %q", sender.From())
	}
	if err := sender.Send(context.Background(), Message{To: "x@example.org"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*body, "From: hello@example.org") {
		t.Errorf("the from address was not used:\n%s", *body)
	}
}

func TestNoServerMeansNoSender(t *testing.T) {
	if _, err := New("", "x@example.org"); err != ErrNotConfigured {
		t.Errorf("an empty url gave %v", err)
	}
	if _, err := New("smtp://", "x@example.org"); err == nil {
		t.Error("an address with no host was accepted")
	}
}

func TestSendingThroughNothingIsAnError(t *testing.T) {
	var sender *Sender
	if err := sender.Send(context.Background(), Message{To: "x@example.org"}); err != ErrNotConfigured {
		t.Errorf("sending with no sender gave %v", err)
	}
}
