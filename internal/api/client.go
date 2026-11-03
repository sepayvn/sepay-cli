// Package api is the SePay API v2 HTTP client.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sepayvn/sepay-cli/internal/build"
)

// APIVersion is the API version pinned on every request.
const APIVersion = "2025-07-01"

// MaxResponseBytes caps a response body so a hostile host can't OOM the client.
const MaxResponseBytes = 10 << 20

// maxBackoff caps one 429 wait against a hostile Retry-After.
const maxBackoff = 60 * time.Second

// UserAgent identifies the CLI build.
func UserAgent() string { return "sepay-cli/" + build.Version }

// ReadBody reads and closes a capped response body; a truncated read is an error, not an empty success.
func ReadBody(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		host := ""
		if resp.Request != nil {
			host = resp.Request.URL.Host
		}
		return nil, NewReadError(host, err)
	}
	if len(body) > MaxResponseBytes {
		return nil, fmt.Errorf("server response exceeded %d bytes", MaxResponseBytes)
	}
	return body, nil
}

// Envelope is the shared v2 response wrapper.
type Envelope struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`

	Data json.RawMessage `json:"data,omitempty"`

	Meta      *Meta             `json:"meta,omitempty"`
	ErrorCode string            `json:"error_code,omitempty"`
	Errors    map[string]string `json:"errors,omitempty"`
}

// Meta holds list-response metadata.
type Meta struct {
	Pagination Pagination `json:"pagination"`
}

// Pagination describes a list response's paging state.
type Pagination struct {
	Total       int  `json:"total"`
	PerPage     int  `json:"per_page"`
	CurrentPage int  `json:"current_page"`
	LastPage    int  `json:"last_page"`
	HasMore     bool `json:"has_more"`
}

// HTTPError is a server-returned error, distinct from a network error.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string

	// Errors holds 422 field-level messages.
	Errors map[string]string
}

func (e *HTTPError) Error() string {
	s := fmt.Sprintf("API error %d", e.StatusCode)
	if e.Code != "" {
		s += " (" + e.Code + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// IsNotFound reports whether err is an API 404.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.StatusCode == http.StatusNotFound
}

// WithRequestID fills an *HTTPError's missing RequestID from X-Request-Id.
func WithRequestID(err error, h http.Header) error {
	var he *HTTPError
	if errors.As(err, &he) && he.RequestID == "" {
		he.RequestID = h.Get("X-Request-Id")
	}
	return err
}

// Client calls the SePay API v2 over HTTP.
type Client struct {
	httpClient *http.Client
	baseURL    string

	mu          sync.Mutex
	token       string
	reauthTried bool

	MaxRetries int

	// Reauth returns a replacement token after a 401 ("" for none). Any verb is replayed:
	// the 401 comes from the auth filter before a handler runs.
	Reauth func(ctx context.Context, rejected string) string

	// Logf, if set, reports progress such as a 429 retry.
	Logf func(format string, args ...any)
}

// NoRedirect refuses redirects so the Bearer token is never resent to another host.
func NoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// NewClient returns a Client for baseURL authenticated with token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: NoRedirect, Transport: DebugTransport(nil)},
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		MaxRetries: 3,
	}
}

// WriteOptions configures a mutating request (Post/Put/Delete).
type WriteOptions struct {
	// Body is JSON-encoded; a json.RawMessage is sent verbatim.
	Body any
	// IdempotencyKey is sent as X-Idempotency-Key and reused across retries.
	IdempotencyKey string
	// Header is applied after the standard headers, so it can override them.
	Header http.Header
}

// BaseURL returns the resolved API base, including its version segment.
func (c *Client) BaseURL() string { return c.baseURL }

// Get sends GET path and returns the unwrapped envelope.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Envelope, error) {
	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	return c.do(ctx, http.MethodGet, fullURL, WriteOptions{})
}

// Post sends POST path with an optional JSON body and returns the envelope.
func (c *Client) Post(ctx context.Context, path string, opts WriteOptions) (*Envelope, error) {
	return c.do(ctx, http.MethodPost, c.baseURL+path, opts)
}

// Put sends PUT path with an optional JSON body and returns the envelope.
func (c *Client) Put(ctx context.Context, path string, opts WriteOptions) (*Envelope, error) {
	return c.do(ctx, http.MethodPut, c.baseURL+path, opts)
}

// Delete sends DELETE path and returns the envelope (empty on 204).
func (c *Client) Delete(ctx context.Context, path string, opts WriteOptions) (*Envelope, error) {
	return c.do(ctx, http.MethodDelete, c.baseURL+path, opts)
}

// Request sends any method to path and returns the unwrapped envelope.
func (c *Client) Request(ctx context.Context, method, path string, query url.Values, opts WriteOptions) (*Envelope, error) {
	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	return c.do(ctx, strings.ToUpper(method), fullURL, opts)
}

func (c *Client) do(ctx context.Context, method, fullURL string, opts WriteOptions) (*Envelope, error) {
	if HasDotSegment(strings.TrimPrefix(fullURL, c.baseURL)) {
		return nil, ErrDotSegment
	}
	var bodyBytes []byte
	if opts.Body != nil {
		var err error
		if bodyBytes, err = json.Marshal(opts.Body); err != nil {
			return nil, fmt.Errorf("could not encode request: %w", err)
		}
	}

	// POST is retried only with an idempotency key.
	canRetry := method != http.MethodPost || opts.IdempotencyKey != ""
	// When the outcome is unknown, a conditional write without a key is not resent: if the first one
	// landed, its If-Match no longer matches and the repeat answers 409 for a write that succeeded.
	canResend := canRetry && (opts.IdempotencyKey != "" || opts.Header.Get("If-Match") == "")

	for attempt := 0; ; attempt++ {
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
		if err != nil {
			return nil, err
		}
		sentToken := c.currentToken()
		req.Header.Set("Authorization", "Bearer "+sentToken)
		req.Header.Set("X-SePay-Api-Version", APIVersion)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", UserAgent())
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if opts.IdempotencyKey != "" {
			req.Header.Set("X-Idempotency-Key", opts.IdempotencyKey)
		}
		for name, vals := range opts.Header {
			req.Header[http.CanonicalHeaderKey(name)] = vals
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			err = NewTransportError(req.URL.Host, c.httpClient.Timeout, err)
			// A transport error leaves the outcome unknown, so retry only when the server can recognise
			// the repeat. ctx.Err() keeps Ctrl-C from being retried; the client's own timeout is retried.
			if canResend && attempt < c.MaxRetries && ctx.Err() == nil {
				wait := Backoff("", attempt)
				c.logRetry(err, wait, attempt)
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, err
		}
		body, err := ReadBody(resp)
		if err != nil {
			// A truncated response is equally unknown; the retry replays the memoized answer.
			if canResend && attempt < c.MaxRetries && ctx.Err() == nil {
				wait := Backoff("", attempt)
				c.logRetry(err, wait, attempt)
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, err
		}

		if resp.Header.Get("X-Idempotent-Replay") == "true" && c.Logf != nil {
			c.Logf("Server replayed the result of an earlier identical request (idempotency key matched).")
		}

		if resp.StatusCode == http.StatusUnauthorized && c.renew(ctx, sentToken) {
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests && canRetry && attempt < c.MaxRetries && !isExhaustedQuota(body) {
			wait := Backoff(resp.Header.Get("Retry-After"), attempt)
			if c.Logf != nil {
				c.Logf("Rate limited (429). Waiting %s before retry (attempt %d)...", wait, attempt+1)
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// The API never redirects an authenticated call, so a 30x is an error.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, &HTTPError{StatusCode: resp.StatusCode, Message: "unexpected redirect from API host", RequestID: resp.Header.Get("X-Request-Id")}
		}

		if resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
			if resp.StatusCode >= 400 {
				return nil, &HTTPError{StatusCode: resp.StatusCode, RequestID: resp.Header.Get("X-Request-Id")}
			}
			return &Envelope{Status: "success"}, nil
		}

		var env Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			// Status before body: an HTML proxy/WAF page must surface its HTTP status, not a JSON error.
			if resp.StatusCode >= 400 {
				return nil, &HTTPError{
					StatusCode: resp.StatusCode,
					Message:    http.StatusText(resp.StatusCode),
					RequestID:  resp.Header.Get("X-Request-Id"),
				}
			}
			return nil, fmt.Errorf("API host %s returned a non-JSON response (HTTP %d)", c.baseURL, resp.StatusCode)
		}

		if resp.StatusCode >= 400 || env.Status == "error" {
			return nil, &HTTPError{
				StatusCode: resp.StatusCode,
				Code:       env.ErrorCode,
				Message:    env.Message,
				RequestID:  resp.Header.Get("X-Request-Id"),
				Errors:     env.Errors,
			}
		}
		return &env, nil
	}
}

// logRetry names a transport failure in plain words; the Go error chain stays out of the terminal.
func (c *Client) logRetry(err error, wait time.Duration, attempt int) {
	if c.Logf == nil {
		return
	}
	reason := err.Error()
	var te *TransportError
	if errors.As(err, &te) {
		reason = te.Reason()
	}
	c.Logf("cannot reach SePay (%s), retrying in %s (attempt %d of %d)...", reason, wait, attempt+1, c.MaxRetries)
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// renew holds the lock across Reauth so concurrent 401s share one rotating refresh.
func (c *Client) renew(ctx context.Context, rejected string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != rejected {
		return true
	}
	if c.Reauth == nil || c.reauthTried {
		return false
	}
	c.reauthTried = true
	fresh := c.Reauth(ctx, rejected)
	if fresh == "" || fresh == rejected {
		return false
	}
	c.token = fresh
	return true
}

// Backoff returns the 429 wait: Retry-After seconds, else 1,2,4,... by attempt, capped at maxBackoff.
// Capped in seconds before converting, since a large value overflows int64 nanoseconds.
func Backoff(retryAfter string, attempt int) time.Duration {
	const capSecs = int(maxBackoff / time.Second)
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		return time.Duration(min(secs, capSecs)) * time.Second
	}
	attempt = min(max(attempt, 0), 6)
	return time.Duration(min(1<<attempt, capSecs)) * time.Second
}

// isExhaustedQuota reports whether a 429 is a spent quota, which backoff never clears.
func isExhaustedQuota(body []byte) bool {
	var env struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	return env.ErrorCode == "quota_exceeded"
}
