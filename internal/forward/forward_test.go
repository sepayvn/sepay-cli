package forward_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/forward"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSend_2xxAndNon2xx(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"ok", http.StatusOK, "ok"},
		{"handler error", http.StatusInternalServerError, "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			fwd, err := forward.NewForwarder(srv.URL, false)
			require.NoError(t, err)
			res, err := fwd.Send(context.Background(), []byte(`{}`), nil)
			require.NoError(t, err)
			require.Equal(t, tt.status, res.StatusCode)
			require.Equal(t, tt.status < 300, res.OK(), "OK() for status %d", tt.status)
			require.Equal(t, tt.body, string(res.Body))
		})
	}
}

func TestSend_ForwardsPayloadAndHeadersVerbatim(t *testing.T) {
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Sepay-Signature")
	}))
	defer srv.Close()

	fwd, err := forward.NewForwarder(srv.URL, false)
	require.NoError(t, err)
	payload := []byte(`{"type":"payment_in","transferAmount":100000}`)
	headers := http.Header{"X-Sepay-Signature": {"sig-abc123"}}
	_, err = fwd.Send(context.Background(), payload, headers)
	require.NoError(t, err)
	require.Equal(t, string(payload), string(gotBody))
	require.Equal(t, "sig-abc123", gotSig)
}

func TestSend_ConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing listens now

	fwd, err := forward.NewForwarder(addr, false)
	require.NoError(t, err)
	_, err = fwd.Send(context.Background(), []byte(`{}`), nil)
	require.Error(t, err, "want a transport error")
}

func TestNewForwarder_Normalize(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantURL  string
		loopback bool
		wantErr  bool
	}{
		{"host:port defaults http", "localhost:3000", "http://localhost:3000/", true, false},
		{"loopback ip", "127.0.0.1:8080", "http://127.0.0.1:8080/", true, false},
		{"remote https keeps path", "https://example.com/hook", "https://example.com/hook", false, false},
		{"non-http scheme rejected", "gopher://evil:70", "", false, true},
		{"empty", "", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fwd, err := forward.NewForwarder(tt.addr, false)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantURL, fwd.Target())
			require.Equal(t, tt.loopback, fwd.IsLoopback())
		})
	}
}

func TestNewForwarder_IsPlaintextRemote(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"localhost:3000", false},
		{"127.0.0.1:8080", false},
		{"https://example.com/hook", false},
		{"http://example.com/hook", true},
		{"example.com:3000", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			fwd, err := forward.NewForwarder(tt.addr, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, fwd.IsPlaintextRemote())
		})
	}
}

func TestNewForwarder_SkipTLSVerifyIsLoopbackOnly(t *testing.T) {
	tests := []struct {
		addr    string
		allowed bool
	}{
		{"https://localhost:3443", true},
		{"https://127.0.0.1:3443/hook", true},
		{"https://[::1]:3443", true},
		{"localhost:3000", true},
		{"https://staging.example.com/hook", false},
		{"https://10.0.0.5:8443", false},
		{"http://staging.example.com/hook", false},
		{"https://localhost.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			_, err := forward.NewForwarder(tt.addr, true)
			if tt.allowed {
				require.NoError(t, err)
				return
			}
			require.Error(t, err, "NewForwarder(%q, skip) accepted the target", tt.addr)
			require.ErrorContains(t, err, "--skip-tls-verify", "error does not name the flag")
		})
	}
}

// Without the flag the same handler must fail, or the test proves nothing.
func TestSend_SkipTLSVerifyReachesASelfSignedLoopbackHandler(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	strict, err := forward.NewForwarder(srv.URL, false)
	require.NoError(t, err)
	_, err = strict.Send(context.Background(), []byte(`{}`), nil)
	require.Error(t, err, "a self-signed handler passed verification without --skip-tls-verify")
	require.False(t, strict.IsUnverifiedTLS(), "IsUnverifiedTLS() without the flag")

	lax, err := forward.NewForwarder(srv.URL, true)
	require.NoError(t, err)
	res, err := lax.Send(context.Background(), []byte(`{}`), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.True(t, lax.IsUnverifiedTLS())
}

func TestForwarder_OriginCollapsesTheSpellingsOfOneHandler(t *testing.T) {
	origin := func(addr string) string {
		t.Helper()
		f, err := forward.NewForwarder(addr, false)
		require.NoError(t, err, "NewForwarder(%q)", addr)
		return f.Origin()
	}

	same := []string{
		"localhost:3000", "http://localhost:3000", "http://localhost:3000/",
		"127.0.0.1:3000", "http://127.0.0.1:3000/", "LOCALHOST:3000", "[::1]:3000",
	}
	for _, pair := range [][2]string{{"localhost:80", "http://localhost"}, {"https://localhost:443/", "https://localhost/"}} {
		assert.Equal(t, origin(pair[1]), origin(pair[0]), "origin(%q) and origin(%q) name the same handler", pair[0], pair[1])
	}
	want := origin(same[0])
	for _, addr := range same[1:] {
		assert.Equal(t, want, origin(addr), "origin(%q) names the same handler", addr)
	}

	for _, addr := range []string{
		"localhost:4000", "localhost:3000/hooks", "https://localhost:3000/", "example.test:3000",
	} {
		assert.NotEqual(t, want, origin(addr), "origin(%q) is a different handler", addr)
	}
}

func TestFailure_TellsWhyTheHandlerDidNotAnswer(t *testing.T) {
	// Port 1 refuses; a just-closed httptest port can be taken by a test running in parallel.
	closedURL := "http://127.0.0.1:1"

	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)

	send := func(t *testing.T, addr string, timeout time.Duration) error {
		t.Helper()
		fwd, err := forward.NewForwarder(addr, false)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_, err = fwd.Send(ctx, []byte("{}"), http.Header{})
		require.Error(t, err)
		return err
	}

	assert.Equal(t, "not responding (is your handler running?)", forward.Failure(send(t, closedURL, 5*time.Second)))
	assert.Equal(t, "failed TLS verification (an untrusted or mismatched certificate)", forward.Failure(send(t, tlsSrv.URL, 5*time.Second)))
	assert.Equal(t, "did not answer in time", forward.Failure(send(t, slow.URL, 100*time.Millisecond)))
}
