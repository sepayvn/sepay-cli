// Package bankhub is the Bank Hub v1 HTTP client, authenticated with a broker-minted product credential.
package bankhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/product"
)

type credentialSource interface {
	Get(ctx context.Context, product string) (*product.Credential, error)
	Refresh(ctx context.Context, product string) (*product.Credential, error)
}

// Client calls the Bank Hub v1 REST API over HTTP.
type Client struct {
	httpClient *http.Client
	baseURL    string
	creds      credentialSource

	MaxRetries int

	// Logf, if set, reports progress such as a 429 retry.
	Logf func(format string, args ...any)
}

// NewClient returns a Bank Hub client for baseURL, minting its Bearer credential from creds.
func NewClient(baseURL string, creds credentialSource) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: api.NoRedirect, Transport: api.DebugTransport(nil)},
		baseURL:    strings.TrimRight(baseURL, "/"),
		creds:      creds,
		MaxRetries: 3,
	}
}

// Response is a Bank Hub v1 success body. Some endpoints skip the "data" wrapper, so Raw keeps the whole body.
type Response struct {
	Raw     json.RawMessage
	Data    json.RawMessage
	Meta    *Meta
	Message string
}

// Meta is a Bank Hub list response's paging block.
type Meta struct {
	Total       int  `json:"total"`
	PerPage     int  `json:"per_page"`
	CurrentPage int  `json:"current_page"`
	PageCount   int  `json:"page_count"`
	HasMore     bool `json:"has_more"`
}

// Into unmarshals the whole response body into v.
func (r *Response) Into(v any) error {
	if len(r.Raw) == 0 {
		return fmt.Errorf("empty response body")
	}
	if err := json.Unmarshal(r.Raw, v); err != nil {
		return fmt.Errorf("could not decode Bank Hub response: %w", err)
	}
	return nil
}

// Get sends GET path and returns the parsed response.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return c.do(ctx, http.MethodGet, path, nil)
}

// Post sends POST path with an optional JSON body and returns the response.
func (c *Client) Post(ctx context.Context, path string, body any) (*Response, error) {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("could not encode request: %w", err)
		}
	}
	return c.do(ctx, http.MethodPost, path, b)
}

func (c *Client) do(ctx context.Context, method, path string, bodyBytes []byte) (*Response, error) {
	if api.HasDotSegment(path) {
		return nil, api.ErrDotSegment
	}
	fullURL := c.baseURL + path
	refreshed := false

	for attempt := 0; ; attempt++ {
		token, err := c.bearer(ctx, refreshed)
		if err != nil {
			return nil, err
		}

		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", api.UserAgent())
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, api.NewTransportError(req.URL.Host, c.httpClient.Timeout, err)
		}
		body, err := api.ReadBody(resp)
		if err != nil {
			return nil, err
		}

		// Re-mint once but replay only a safe method: there is no idempotency key on this host.
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed = true
			if method == http.MethodGet {
				continue
			}
			if _, rerr := c.creds.Refresh(ctx, product.BankHub); rerr != nil && c.Logf != nil {
				c.Logf("Could not replace the rejected credential (%v); the next call will try again.", rerr)
			}
			return nil, api.WithRequestID(parseError(resp.StatusCode, body), resp.Header)
		}
		// Retry only GETs: a POST has no idempotency key.
		if resp.StatusCode == http.StatusTooManyRequests && method == http.MethodGet && attempt < c.MaxRetries {
			wait := api.Backoff(resp.Header.Get("Retry-After"), attempt)
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
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, &api.HTTPError{StatusCode: resp.StatusCode, Message: "unexpected redirect from Bank Hub host", RequestID: resp.Header.Get("X-Request-Id")}
		}
		if resp.StatusCode >= 400 {
			return nil, api.WithRequestID(parseError(resp.StatusCode, body), resp.Header)
		}
		return parseSuccess(body)
	}
}

func (c *Client) bearer(ctx context.Context, forceRefresh bool) (string, error) {
	var cred *product.Credential
	var err error
	if forceRefresh {
		cred, err = c.creds.Refresh(ctx, product.BankHub)
	} else {
		cred, err = c.creds.Get(ctx, product.BankHub)
	}
	if err != nil {
		return "", err
	}
	return cred.AccessToken, nil
}

func parseSuccess(body []byte) (*Response, error) {
	r := &Response{Raw: body}
	if len(bytes.TrimSpace(body)) == 0 {
		return r, nil
	}
	var raw struct {
		Data    json.RawMessage `json:"data"`
		Meta    *Meta           `json:"meta"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("could not read Bank Hub response: %w", err)
	}
	r.Data, r.Meta, r.Message = raw.Data, raw.Meta, raw.Message
	return r, nil
}

// parseError maps Bank Hub's three error shapes onto an *api.HTTPError, status first:
//
//	(a) {code, message, errors}          standard; "errors" is a field map on 400/422
//	(b) {status, error, messages}        framework default (link-token/*, company edit no-body)
//	(c) {code: "STRING", message}        string code (link-token get/revoke 404)
func parseError(status int, body []byte) error {
	var p struct {
		Code     json.RawMessage   `json:"code"`     // int (a) or string (c)
		Message  string            `json:"message"`  // (a), (c)
		Errors   map[string]string `json:"errors"`   // (a) field validation
		Messages map[string]string `json:"messages"` // (b)
	}
	_ = json.Unmarshal(body, &p)

	msg := p.Message
	if msg == "" && len(p.Messages) > 0 {
		msg = firstMessage(p.Messages)
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	code := codeString(p.Code)
	// Shape (a) echoes the status as code; on a 5xx that would hide the server_error fallback.
	if status >= 500 && code == strconv.Itoa(status) {
		code = ""
	}
	return &api.HTTPError{StatusCode: status, Code: code, Message: msg, Errors: p.Errors}
}

// codeString reads "code", sent as an int (shape a) or a string (shape c).
func codeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func firstMessage(m map[string]string) string {
	if v, ok := m["error"]; ok {
		return v
	}
	for _, v := range m {
		return v
	}
	return ""
}
