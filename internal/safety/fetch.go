package safety

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultMaxBytes     = 5 << 20
	DefaultMaxRedirects = 3
	DefaultTimeout      = 30 * time.Second
	DefaultDialTimeout  = 10 * time.Second
)

var (
	ErrTooLarge      = errors.New("response exceeds size limit")
	ErrContentType   = errors.New("unacceptable content type")
	ErrTooManyHops   = errors.New("too many redirects")
	ErrScheme        = errors.New("scheme not allowed")
	ErrURLCredential = errors.New("credentials in url")
)

type Options struct {
	MaxBytes           int64
	MaxRedirects       int
	Timeout            time.Duration
	UserAgent          string
	AcceptContentTypes []string
	AllowPrivateAddrs  bool
}

type Fetcher struct {
	client   *http.Client
	maxBytes int64
	accept   []string
	agent    string
}

type Result struct {
	URL        *url.URL
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (r *Result) NotModified() bool { return r.StatusCode == http.StatusNotModified }
func (r *Result) ETag() string      { return r.Header.Get("ETag") }
func (r *Result) LastModified() string {
	return r.Header.Get("Last-Modified")
}

func New(o Options) *Fetcher {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = DefaultMaxRedirects
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.UserAgent == "" {
		o.UserAgent = "rss-expert"
	}

	dialer := &net.Dialer{Timeout: DefaultDialTimeout, KeepAlive: 30 * time.Second}
	if !o.AllowPrivateAddrs {
		dialer.Control = controlAddr
	}

	accept := make([]string, len(o.AcceptContentTypes))
	for i, a := range o.AcceptContentTypes {
		accept[i] = strings.ToLower(a)
	}

	return &Fetcher{
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           dialer.DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
			CheckRedirect: redirectGuard(o.MaxRedirects),
			Timeout:       o.Timeout,
		},
		maxBytes: o.MaxBytes,
		accept:   accept,
		agent:    o.UserAgent,
	}
}

func (f *Fetcher) Get(ctx context.Context, rawURL string, header http.Header) (*Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if err := CheckURL(u); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", f.agent)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, unwrapURLError(err)
	}
	defer resp.Body.Close()

	final := resp.Request.URL
	if resp.StatusCode == http.StatusNotModified {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return &Result{URL: final, StatusCode: resp.StatusCode, Header: resp.Header}, nil
	}
	if resp.StatusCode < 400 {
		if err := f.checkContentType(resp.Header.Get("Content-Type")); err != nil {
			return nil, err
		}
	}
	body, err := readLimited(resp.Body, f.maxBytes)
	if err != nil {
		return nil, err
	}
	return &Result{URL: final, StatusCode: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

func CheckURL(u *url.URL) error {
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("%w: %q", ErrScheme, u.Scheme)
	}
	if u.User != nil {
		return ErrURLCredential
	}
	if u.Hostname() == "" {
		return errors.New("missing host")
	}
	return nil
}

func controlAddr(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("network %q not allowed", network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", address, err)
	}
	return CheckAddr(ap.Addr())
}

func redirectGuard(max int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > max {
			return fmt.Errorf("%w: %d", ErrTooManyHops, len(via))
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return CheckURL(req.URL)
	}
}

func (f *Fetcher) checkContentType(v string) error {
	if len(f.accept) == 0 || v == "" {
		return nil
	}
	mt, _, err := mime.ParseMediaType(v)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrContentType, v)
	}
	mt = strings.ToLower(mt)
	for _, a := range f.accept {
		if mt == a {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrContentType, mt)
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, max)
	}
	return b, nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func (f *Fetcher) Do(req *http.Request) (*http.Response, error) {
	if err := CheckURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.agent)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, unwrapURLError(err)
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, f.maxBytes), resp.Body}
	return resp, nil
}
