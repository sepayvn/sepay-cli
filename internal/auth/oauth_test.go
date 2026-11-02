package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func oauthError(w http.ResponseWriter, code string) {
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
}

func TestRequestDeviceCode(t *testing.T) {
	var gotChallenge, gotMethod, gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotChallenge = r.FormValue("code_challenge")
		gotMethod = r.FormValue("code_challenge_method")
		gotClient = r.FormValue("client_id")
		_, _ = w.Write([]byte(`{"device_code":"dc1","user_code":"WDJB-MJHT","verification_uri":"https://x/activate","interval":5,"expires_in":900}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	da, pkce, err := c.RequestDeviceCode(context.Background(), []string{"sandbox"})
	require.NoError(t, err)

	assert.Equal(t, "dc1", da.DeviceCode)
	assert.Equal(t, "WDJB-MJHT", da.UserCode)
	assert.Equal(t, 5, da.Interval)
	assert.Equal(t, "sepay-cli", gotClient)
	assert.Equal(t, "S256", gotMethod)
	assert.Equal(t, pkce.Challenge, gotChallenge, "the challenge sent must be the one we generated")
}

func TestPollToken_PendingThenApproved(t *testing.T) {
	var polls, gotVerifier atomicState
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier.str = r.FormValue("code_verifier")
		if polls.n.Add(1) <= 2 {
			oauthError(w, CodeAuthorizationPending)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","refresh_token":"rt","scope":"sandbox","expires_in":3600}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	tok, err := c.PollToken(context.Background(), "dc1", "verifier-xyz", time.Millisecond, time.Second)
	require.NoError(t, err)

	assert.Equal(t, "at", tok.AccessToken)
	assert.Equal(t, "rt", tok.RefreshToken)
	assert.EqualValues(t, 3, polls.n.Load(), "should poll twice pending, then succeed")
	assert.Equal(t, "verifier-xyz", gotVerifier.str)
}

func TestPollToken_SlowDown(t *testing.T) {
	var polls atomicState
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if polls.n.Add(1) == 1 {
			oauthError(w, CodeSlowDown)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	c.SlowDownIncrement = time.Millisecond // don't wait the real 5s in a unit test
	tok, err := c.PollToken(context.Background(), "dc1", "v", time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "at", tok.AccessToken)
}

func TestPollToken_TerminalErrors(t *testing.T) {
	for _, code := range []string{CodeExpiredToken, CodeAccessDenied} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				oauthError(w, code)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "sepay-cli", srv.Client())
			_, err := c.PollToken(context.Background(), "dc1", "v", time.Millisecond, time.Second)

			var authErr *Error
			require.ErrorAs(t, err, &authErr)
			assert.Equal(t, code, authErr.Code)
		})
	}
}

func TestPollToken_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oauthError(w, CodeAuthorizationPending) // never approves
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	_, err := c.PollToken(ctx, "dc1", "v", 10*time.Millisecond, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestPollToken_ClientDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oauthError(w, CodeAuthorizationPending) // server never times out
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	// No ctx deadline: only the client-side expiresIn can stop this.
	_, err := c.PollToken(context.Background(), "dc1", "v", 5*time.Millisecond, 25*time.Millisecond)

	var authErr *Error
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, CodeExpiredToken, authErr.Code)
}

func TestRefreshAndRevoke(t *testing.T) {
	var gotGrant, gotToken, gotHint string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/oauth/token":
			gotGrant = r.FormValue("grant_type")
			_, _ = w.Write([]byte(`{"access_token":"at2","refresh_token":"rt2","scope":"read"}`))
		case "/oauth/revoke":
			gotToken = r.FormValue("token")
			gotHint = r.FormValue("token_type_hint")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())

	tok, err := c.Refresh(context.Background(), "rt", []string{"read"})
	require.NoError(t, err)
	assert.Equal(t, "at2", tok.AccessToken)
	assert.Equal(t, "refresh_token", gotGrant)

	require.NoError(t, c.Revoke(context.Background(), "at2", TokenTypeAccess))
	assert.Equal(t, "at2", gotToken)
	assert.Equal(t, TokenTypeAccess, gotHint, "Revoke must send token_type_hint")
}

func TestPostForm_NonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body>login</body></html>"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	_, _, err := c.RequestDeviceCode(context.Background(), []string{"sandbox"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-JSON response")
	assert.NotContains(t, err.Error(), "invalid character")
}

type atomicState struct {
	n   atomic.Int64
	str string
}
