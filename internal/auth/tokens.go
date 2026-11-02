package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxResponseBytes caps a token-management response body.
const maxResponseBytes = 1 << 20

// TokenSession is one OAuth session from GET /oauth/tokens; token values are never returned.
type TokenSession struct {
	ID         string   `json:"id"`
	Scopes     []string `json:"scopes"`
	Revoked    bool     `json:"revoked"`
	Current    bool     `json:"current"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at"`
	LastUsedAt string   `json:"last_used_at"`
}

// ListTokens returns the caller's OAuth sessions.
func (c *Client) ListTokens(ctx context.Context, accessToken string) ([]TokenSession, error) {
	var out struct {
		Data []TokenSession `json:"data"`
	}
	if err := c.doBearer(ctx, http.MethodGet, "/oauth/tokens", accessToken, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// RevokeTokenID revokes one of the caller's sessions and returns the number revoked.
func (c *Client) RevokeTokenID(ctx context.Context, accessToken, id string) (int, error) {
	return c.revokeSessions(ctx, "/oauth/tokens/revoke", accessToken, url.Values{"id": {id}})
}

// RevokeAllTokens revokes every session of the caller and returns the count.
func (c *Client) RevokeAllTokens(ctx context.Context, accessToken string) (int, error) {
	return c.revokeSessions(ctx, "/oauth/tokens/revoke-all", accessToken, nil)
}

func (c *Client) revokeSessions(ctx context.Context, path, accessToken string, form url.Values) (int, error) {
	var out struct {
		Data struct {
			Revoked int `json:"revoked"`
		} `json:"data"`
	}
	if err := c.doBearer(ctx, http.MethodPost, path, accessToken, form, &out); err != nil {
		return 0, err
	}
	return out.Data.Revoked, nil
}

// doBearer calls a token-management endpoint, which uses the v2 envelope and error shape, not RFC 6749.
func (c *Client) doBearer(ctx context.Context, method, path, accessToken string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}

	if !looksLikeJSON(respBody) {
		return fmt.Errorf("oauth host %s returned a non-JSON response (HTTP %d); check --hostname or SEPAY_OAUTH_URL", c.baseURL, resp.StatusCode)
	}
	// Not just >= 400: an unauthenticated GET gets a 302 with an empty body, which would decode as no sessions.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			ErrorCode string `json:"error_code"`
			Message   string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &e)
		if e.ErrorCode == "" {
			e.ErrorCode = "unknown_error"
		}
		if e.Message == "" && resp.StatusCode >= 300 && resp.StatusCode < 400 {
			e.Message = "the OAuth host redirected instead of answering; the session is not valid for this host"
		}
		return &Error{Code: e.ErrorCode, Description: e.Message, StatusCode: resp.StatusCode}
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("could not read OAuth response: %w", err)
	}
	return nil
}
