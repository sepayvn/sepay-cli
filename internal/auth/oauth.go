package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// Token type hints for Revoke (RFC 7009 §2.1); "" omits the hint.
const (
	TokenTypeAccess  = "access_token"
	TokenTypeRefresh = "refresh_token"
)

// Client calls the OAuth host: device grant, refresh, and revoke.
type Client struct {
	httpClient *http.Client
	baseURL    string
	clientID   string

	// SlowDownIncrement is added on each slow_down (RFC 8628 §3.5).
	SlowDownIncrement time.Duration
}

// NewClient returns an OAuth Client for baseURL as clientID; a nil httpClient gets a default.
func NewClient(baseURL, clientID string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: api.NoRedirect, Transport: api.DebugTransport(nil)}
	}
	return &Client{
		httpClient:        httpClient,
		baseURL:           strings.TrimRight(baseURL, "/"),
		clientID:          clientID,
		SlowDownIncrement: 5 * time.Second,
	}
}

// BaseURL returns the resolved OAuth host; login stores it so refresh reaches the issuing host.
func (c *Client) BaseURL() string { return c.baseURL }

// DeviceAuth is the response to a device-authorization request (RFC 8628 §3.2).
type DeviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Token is an issued token pair; Email/Name are set when the server includes them.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Email        string `json:"email"`
	Name         string `json:"name"`
}

// RequestDeviceCode starts the device flow and returns the response and the PKCE pair.
func (c *Client) RequestDeviceCode(ctx context.Context, scopes []string) (*DeviceAuth, *PKCE, error) {
	pkce, err := NewPKCE()
	if err != nil {
		return nil, nil, err
	}
	form := url.Values{
		"client_id":             {c.clientID},
		"scope":                 {strings.Join(scopes, " ")},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {challengeMethodS256},
	}
	var da DeviceAuth
	if err := c.postForm(ctx, "/oauth/device", form, &da); err != nil {
		return nil, nil, err
	}
	return &da, pkce, nil
}

// PollToken polls until approval, denial, ctx cancel, or expiresIn (a client-side deadline;
// <= 0 disables it), honouring interval and slow_down (RFC 8628 §3.5).
func (c *Client) PollToken(ctx context.Context, deviceCode, verifier string, interval, expiresIn time.Duration) (*Token, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var expired <-chan time.Time
	if expiresIn > 0 {
		t := time.NewTimer(expiresIn)
		defer t.Stop()
		expired = t.C
	}
	form := url.Values{
		"grant_type":    {deviceGrantType},
		"device_code":   {deviceCode},
		"client_id":     {c.clientID},
		"code_verifier": {verifier},
	}
	for {
		var tok Token
		err := c.postForm(ctx, "/oauth/token", form, &tok)
		if err == nil {
			return &tok, nil
		}

		var authErr *Error
		if !errors.As(err, &authErr) {
			return nil, err
		}
		switch authErr.Code {
		case CodeAuthorizationPending:
		case CodeSlowDown:
			interval += c.SlowDownIncrement
		default:
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-expired:
			return nil, &Error{Code: CodeExpiredToken}
		case <-time.After(interval):
		}
	}
}

// Refresh exchanges a refresh token for a new access token, optionally changing scopes.
func (c *Client) Refresh(ctx context.Context, refreshToken string, scopes []string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID},
	}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	var tok Token
	if err := c.postForm(ctx, "/oauth/token", form, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// Revoke invalidates a token (RFC 7009); tokenTypeHint may be "".
func (c *Client) Revoke(ctx context.Context, token, tokenTypeHint string) error {
	form := url.Values{
		"token":     {token},
		"client_id": {c.clientID},
	}
	if tokenTypeHint != "" {
		form.Set("token_type_hint", tokenTypeHint)
	}
	return c.postForm(ctx, "/oauth/revoke", form, nil)
}

// postForm turns a 4xx/5xx into an *Error carrying the RFC error code.
func (c *Client) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", api.UserAgent())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return api.NewTransportError(req.URL.Host, c.httpClient.Timeout, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return api.NewReadError(req.URL.Host, err)
	}

	// Redirects are not followed; an empty 3xx body must not read as an empty token.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("oauth host %s redirected (HTTP %d); check --hostname or SEPAY_OAUTH_URL", c.baseURL, resp.StatusCode)
	}

	// A non-JSON body means the /oauth endpoints aren't served on this host.
	if !looksLikeJSON(body) {
		return fmt.Errorf("oauth host %s returned a non-JSON response (HTTP %d); check --hostname or SEPAY_OAUTH_URL", c.baseURL, resp.StatusCode)
	}

	if resp.StatusCode >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
			Message     string `json:"message"`
			Hint        string `json:"hint"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error == "" {
			e.Error = "unknown_error"
		}
		// The token endpoint sends {error, message, hint}; hint is the specific one.
		detail := e.Description
		if detail == "" {
			detail = e.Hint
		}
		if detail == "" {
			detail = e.Message
		}
		return &Error{Code: e.Error, Description: detail, StatusCode: resp.StatusCode}
	}

	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("could not read OAuth response: %w", err)
	}
	return nil
}

// looksLikeJSON treats an empty body as JSON so an empty 200 (revoke) succeeds.
func looksLikeJSON(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return true
	}
	return body[0] == '{' || body[0] == '['
}
