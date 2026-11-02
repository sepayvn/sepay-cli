// Package forward replays a webhook event verbatim, headers included, to a local handler.
package forward

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxSnippet = 2 << 10

// Forwarder POSTs webhook events to one local handler address.
type Forwarder struct {
	target    string
	origin    string
	loopback  bool
	plaintext bool
	insecure  bool
	client    *http.Client
}

// Result is the outcome of forwarding one event.
type Result struct {
	StatusCode int
	Latency    time.Duration
	Body       []byte // bounded snippet, for a non-2xx diagnostic
}

// OK reports whether the handler answered with a 2xx status.
func (r *Result) OK() bool { return r.StatusCode >= 200 && r.StatusCode < 300 }

// NewForwarder builds a Forwarder for addr (scheme defaults to http). skipTLSVerify is refused off loopback.
func NewForwarder(addr string, skipTLSVerify bool) (*Forwarder, error) {
	u, err := normalizeAddr(addr)
	if err != nil {
		return nil, err
	}
	loopback := isLoopbackHost(u.Hostname())
	if skipTLSVerify && !loopback {
		return nil, fmt.Errorf("--skip-tls-verify only applies to a loopback forward address, got %s", u.Host)
	}

	transport := http.DefaultTransport
	insecure := skipTLSVerify && u.Scheme == "https"
	if insecure {
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in, loopback only (checked above)
	}
	return &Forwarder{
		target:    u.String(),
		origin:    originOf(u),
		loopback:  loopback,
		plaintext: u.Scheme == "http",
		insecure:  insecure,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			// A 30x could bounce the webhook and its headers to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Target returns the normalized handler URL.
func (f *Forwarder) Target() string { return f.target }

// Origin returns a stable identity for the handler; Target is for display.
func (f *Forwarder) Origin() string { return f.origin }

// originOf maps every loopback host to "localhost" and lowercases it; scheme, port and path stay.
func originOf(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if isLoopbackHost(host) {
		host = "localhost"
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return u.Scheme + "://" + host + u.EscapedPath()
}

// IsLoopback reports whether the handler is on the local machine.
func (f *Forwarder) IsLoopback() bool { return f.loopback }

// IsPlaintextRemote reports whether the handler is off-machine over plain http.
func (f *Forwarder) IsPlaintextRemote() bool { return !f.loopback && f.plaintext }

// IsUnverifiedTLS reports whether the handler's certificate goes unchecked.
func (f *Forwarder) IsUnverifiedTLS() bool { return f.insecure }

// Send POSTs payload with headers copied verbatim. A non-2xx is a Result; a transport failure is an error.
func (f *Forwarder) Send(ctx context.Context, payload []byte, headers http.Header) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := f.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxSnippet))
	return &Result{StatusCode: resp.StatusCode, Latency: latency, Body: snippet}, nil
}

func normalizeAddr(addr string) (*url.URL, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("forward address is empty")
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid forward address %q: %w", addr, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("forward address must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid forward address %q", addr)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
