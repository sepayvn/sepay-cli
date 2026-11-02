// Package pg is the Payment Gateway v1 HTTP client, authenticated with a broker-minted product credential.
package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/product"
)

type credentialSource interface {
	Get(ctx context.Context, product string) (*product.Credential, error)
	Refresh(ctx context.Context, product string) (*product.Credential, error)
}

// Client calls the Payment Gateway v1 REST API over HTTP.
type Client struct {
	httpClient *http.Client
	baseURL    string
	creds      credentialSource

	MaxRetries int

	// CheckoutURL is the hosted-checkout host, separate from the REST API host.
	CheckoutURL string

	// Logf, if set, reports progress such as a 429 retry.
	Logf func(format string, args ...any)
}

// NewClient returns a PG client for baseURL, minting its Basic credential from creds.
func NewClient(baseURL string, creds credentialSource) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: api.NoRedirect, Transport: api.DebugTransport(nil)},
		baseURL:    strings.TrimRight(baseURL, "/"),
		creds:      creds,
		MaxRetries: 3,
	}
}

// Response is a PG v1 success body. Some endpoints skip the "data" wrapper, so Raw keeps the whole body.
type Response struct {
	Raw     json.RawMessage
	Data    json.RawMessage
	Meta    *Meta
	Message string
}

// Meta is a PG list response's paging block.
type Meta struct {
	PerPage     int  `json:"per_page"`
	Total       int  `json:"total"`
	HasMore     bool `json:"has_more"`
	CurrentPage int  `json:"current_page"`
	PageCount   int  `json:"page_count"`
}

// Into unmarshals the whole response body into v.
func (r *Response) Into(v any) error {
	if len(r.Raw) == 0 {
		return fmt.Errorf("empty response body")
	}
	if err := json.Unmarshal(r.Raw, v); err != nil {
		return fmt.Errorf("could not decode PG response: %w", err)
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
		id, secret, err := c.basic(ctx, refreshed)
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
		req.SetBasicAuth(id, secret)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", api.UserAgent())
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := api.ReadBody(resp)
		if err != nil {
			return nil, err
		}

		// Re-mint once but replay only a safe method (no idempotency key). The re-mint still runs for a
		// mutation: the Basic pair never expires, so the next run would fail alike.
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed = true
			if method == http.MethodGet {
				continue
			}
			if _, rerr := c.creds.Refresh(ctx, product.PG); rerr != nil && c.Logf != nil {
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
		// 307/308 would replay a mutation with the Basic credential elsewhere.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			msg := "unexpected redirect from Payment Gateway host"
			if loc := resp.Header.Get("Location"); loc != "" {
				msg += " to " + loc
			}
			return nil, &api.HTTPError{StatusCode: resp.StatusCode, Message: msg, RequestID: resp.Header.Get("X-Request-Id")}
		}
		if resp.StatusCode >= 400 {
			return nil, api.WithRequestID(parseError(resp.StatusCode, body), resp.Header)
		}
		return parseSuccess(body)
	}
}

func (c *Client) basic(ctx context.Context, forceRefresh bool) (merchantID, secretKey string, err error) {
	var cred *product.Credential
	if forceRefresh {
		cred, err = c.creds.Refresh(ctx, product.PG)
	} else {
		cred, err = c.creds.Get(ctx, product.PG)
	}
	if err != nil {
		return "", "", err
	}
	id, secret := cred.BasicAuth()
	return id, secret, nil
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
		return nil, fmt.Errorf("could not read PG response: %w", err)
	}
	r.Data, r.Meta, r.Message = raw.Data, raw.Meta, raw.Message
	return r, nil
}

// parseError maps PG's {code,message} and framework {status,error,messages} shapes onto an *api.HTTPError.
func parseError(status int, body []byte) error {
	var biz struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
	}
	if json.Unmarshal(body, &biz) == nil && biz.Code != "" && biz.Message != "" {
		return &api.HTTPError{StatusCode: status, Code: biz.Code.String(), Message: biz.Message}
	}

	var ci4 struct {
		Messages map[string]string `json:"messages"`
	}
	msg := ""
	if json.Unmarshal(body, &ci4) == nil && len(ci4.Messages) > 0 {
		if m, ok := ci4.Messages["error"]; ok {
			msg = m
		} else {
			for _, v := range ci4.Messages {
				msg = v
				break
			}
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &api.HTTPError{StatusCode: status, Message: msg}
}
