package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/auth"
)

// StaleTokenRefresh reports whether the last client's token went out stale because refresh was unreachable.
func (f *Factory) StaleTokenRefresh() error { return f.staleTokenRefresh }

// refusedTheGrant: only a transport failure (*url.Error) means the host was never reached; an unreadable
// answer counts as refused so a revoked grant behind a proxy still ends the session. 408, 429 and 5xx settle nothing.
func refusedTheGrant(err error) bool {
	var transport *url.Error
	if errors.As(err, &transport) {
		return false
	}

	var e *auth.Error
	if !errors.As(err, &e) {
		return true
	}

	if e.StatusCode >= 500 || e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests {
		return false
	}

	return e.StatusCode >= 400
}

// grantIsGone is narrower than refusedTheGrant because it deletes the stored token.
func grantIsGone(err error) bool {
	var e *auth.Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == "invalid_grant" || (e.StatusCode == http.StatusUnauthorized && e.Code == "invalid_request")
}

// resolveToken returns the chosen profile's access token, refreshing an expired one first.
func (f *Factory) resolveToken() string {
	f.staleTokenRefresh = nil

	mgr, err := f.Credentials()
	if err != nil {
		return ""
	}
	name := f.ProfileName(mgr.Hosts())
	cred, err := mgr.Get(name)
	if err != nil {
		return ""
	}
	fresh, refreshErr := f.refreshIfExpired(mgr, name, cred)
	f.noteStaleRefresh(refreshErr)
	if fresh != "" {
		return fresh
	}
	if grantIsGone(refreshErr) {
		return ""
	}
	return cred.AccessToken
}

// renewRejectedToken is the API client's Reauth hook.
func (f *Factory) renewRejectedToken(ctx context.Context, rejected string) string {
	mgr, err := f.Credentials()
	if err != nil {
		return ""
	}
	name := f.ProfileName(mgr.Hosts())
	cred, err := mgr.Get(name)
	if err != nil || cred.RefreshToken == "" {
		return ""
	}
	if cred.AccessToken != rejected && !cred.Expired() {
		return cred.AccessToken
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	fresh, err := f.refresh(ctx, mgr, name, cred)
	f.noteStaleRefresh(err)
	return fresh
}

func (f *Factory) noteStaleRefresh(err error) {
	if errors.Is(err, errRefreshExchange) && !refusedTheGrant(err) {
		f.staleTokenRefresh = fmt.Errorf("%w: %v", ErrTokenRefreshUnreachable, err) //nolint:errorlint // an HTTPError in the chain would map to ExitAuth
	}
}

// TokenForProfile returns the OAuth host and a valid access token for a profile, refreshing if lapsed.
func (f *Factory) TokenForProfile(explicit string) (host, accessToken string, err error) {
	mgr, err := f.Credentials()
	if err != nil {
		return "", "", err
	}
	hosts := mgr.Hosts()
	name := explicit
	if name == "" {
		name = f.ProfileName(hosts)
	}
	cred, err := mgr.Get(name)
	if err != nil || cred.AccessToken == "" {
		return "", "", AuthError{Err: errors.New("not logged in, run: sepay login")}
	}
	token := cred.AccessToken
	if fresh, _ := f.refreshIfExpired(mgr, name, cred); fresh != "" {
		token = fresh
	}
	return hosts.Profiles[name].Host, token, nil
}

// refreshIfExpired returns "" on any failure, so the caller sends the stored token and gets a 401.
func (f *Factory) refreshIfExpired(mgr *auth.Manager, profile string, cred *auth.Credential) (string, error) {
	if !cred.Expired() || cred.RefreshToken == "" {
		return "", nil
	}
	base := f.runCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, refreshTimeout)
	defer cancel()
	return f.refresh(ctx, mgr, profile, cred)
}

func (f *Factory) refresh(ctx context.Context, mgr *auth.Manager, profile string, cred *auth.Credential) (string, error) {
	// Under the refresh lock a racing process sees the pair this one stored and spends nothing.
	// SetIfCurrent still settles a concurrent re-login, which does not take the lock.
	if release, ok := mgr.LockRefresh(ctx); ok {
		defer release()
		if current, err := mgr.Current(profile); err == nil && current.RefreshToken != cred.RefreshToken {
			if current.Revoked() {
				return "", nil
			}
			if !current.Expired() {
				return current.AccessToken, nil
			}
			cred = current
		}
	}
	p := mgr.Hosts().Profiles[profile]

	tok, err := f.NewOAuthClient(p.Host).Refresh(ctx, cred.RefreshToken, p.Scopes)
	if err != nil {
		if grantIsGone(err) {
			_ = mgr.MarkRevoked(profile, cred)
		}
		return "", fmt.Errorf("%w: %w", errRefreshExchange, err)
	}
	// A non-rotating endpoint may omit refresh_token: keep the stored one.
	if tok.RefreshToken == "" {
		tok.RefreshToken = cred.RefreshToken
	}
	if _, err := mgr.SetIfCurrent(profile, p.Host, cred, auth.CredentialFromToken(tok), strings.Fields(tok.Scope), p.Insecure); err != nil {
		var changed *auth.ProfileChangedError
		if errors.As(err, &changed) && changed.Held != nil && !changed.Held.Expired() {
			return changed.Held.AccessToken, nil
		}
		if errors.Is(err, auth.ErrKeychainUnavailable) {
			// Nothing reached the file. Use the new token for this run only, and say why the next
			// run may have to sign in again (the server may have rotated the refresh token).
			if !f.keychainWarned {
				f.keychainWarned = true
				f.ErrPrintf("! the renewed token is used for this run only and was not saved: %s\n", err.Error())
			}
			return tok.AccessToken, nil
		}
		return "", err
	}
	return tok.AccessToken, nil
}
