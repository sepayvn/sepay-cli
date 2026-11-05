// Package einvoice is the E-Invoice v1 HTTP client, authenticated with a broker-minted product credential.
package einvoice

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

// Client calls the E-Invoice v1 REST API over HTTP.
type Client struct {
	httpClient *http.Client
	baseURL    string
	creds      credentialSource

	MaxRetries int

	// Logf, if set, reports progress such as a 429 retry.
	Logf func(format string, args ...any)
}

// NewClient returns an E-Invoice client for baseURL, minting its Bearer credential from creds.
func NewClient(baseURL string, creds credentialSource) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: api.NoRedirect, Transport: api.DebugTransport(nil)},
		baseURL:    strings.TrimRight(baseURL, "/"),
		creds:      creds,
		MaxRetries: 3,
	}
}

// Response is an E-Invoice v1 success body; "success" is ignored, the HTTP status decides.
type Response struct {
	Data json.RawMessage
}

// Paging is the paging block nested inside a list response's "data".
type Paging struct {
	PerPage     int  `json:"per_page"`
	Total       int  `json:"total"`
	HasMore     bool `json:"has_more"`
	CurrentPage int  `json:"current_page"`
	PageCount   int  `json:"page_count"`
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

		resp, err := c.httpClient.Do(req) //nolint:bodyclose // api.ReadBody closes it
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
			if _, rerr := c.creds.Refresh(ctx, product.EInvoice); rerr != nil && c.Logf != nil {
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
			return nil, &api.HTTPError{StatusCode: resp.StatusCode, Message: "unexpected redirect from E-Invoice host", RequestID: resp.Header.Get("X-Request-Id")}
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
		cred, err = c.creds.Refresh(ctx, product.EInvoice)
	} else {
		cred, err = c.creds.Get(ctx, product.EInvoice)
	}
	if err != nil {
		return "", err
	}
	return cred.AccessToken, nil
}

func parseSuccess(body []byte) (*Response, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return &Response{}, nil
	}
	var raw struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("could not read E-Invoice response: %w", err)
	}
	return &Response{Data: raw.Data}, nil
}

// parseError maps {success:false, error:{code,message,details}} onto an *api.HTTPError; code is a string enum.
func parseError(status int, body []byte) error {
	var p struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &p)

	msg := p.Error.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &api.HTTPError{StatusCode: status, Code: p.Error.Code, Message: msg, Errors: p.Error.Details}
}
