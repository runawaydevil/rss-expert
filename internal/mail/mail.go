package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

var ErrNotConfigured = errors.New("mail: no server is configured; set RSS_EXPERT_SMTP_URL")

type Sender struct {
	host     string
	port     string
	user     string
	password string
	from     string
	send     func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

type Message struct {
	To      string
	Subject string
	Body    string
}

func New(rawURL, fallbackFrom string) (*Sender, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, ErrNotConfigured
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("mail: unreadable server address: %w", err)
	}

	s := &Sender{
		host: parsed.Hostname(),
		port: parsed.Port(),
		from: fallbackFrom,
		send: smtp.SendMail,
	}
	if s.host == "" {
		return nil, errors.New("mail: the server address has no host")
	}
	if s.port == "" {
		s.port = "587"
		if parsed.Scheme == "smtps" {
			s.port = "465"
		}
	}
	if parsed.User != nil {
		s.user = parsed.User.Username()
		s.password, _ = parsed.User.Password()
	}
	if from := parsed.Query().Get("from"); from != "" {
		s.from = from
	}
	if s.from == "" {
		s.from = "rss-expert@" + s.host
	}
	return s, nil
}

func (s *Sender) From() string { return s.from }

func (s *Sender) Send(ctx context.Context, m Message) error {
	if s == nil {
		return ErrNotConfigured
	}

	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.password, s.host)
	}

	addr := net.JoinHostPort(s.host, s.port)
	body := compose(s.from, m)

	done := make(chan error, 1)
	go func() { done <- s.send(addr, auth, s.from, []string{m.To}, body) }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("mail: send to %s: %w", m.To, err)
		}
		return nil
	}
}

func compose(from string, m Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + m.Subject + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	return []byte(b.String())
}

func (s *Sender) SetSendFunc(fn func(addr string, a smtp.Auth, from string, to []string, msg []byte) error) {
	s.send = fn
}
