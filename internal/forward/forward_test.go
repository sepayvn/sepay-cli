package forward_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/forward"
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
			if err != nil {
				t.Fatalf("NewForwarder: %v", err)
			}
			res, err := fwd.Send(context.Background(), []byte(`{}`), nil)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if res.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.status)
			}
			if res.OK() != (tt.status < 300) {
				t.Fatalf("OK() = %v for status %d", res.OK(), tt.status)
			}
			if string(res.Body) != tt.body {
				t.Fatalf("body = %q, want %q", res.Body, tt.body)
			}
		})
	}
}

func TestSend_ForwardsPayloadAndHeadersVerbatim(t *testing.T) {
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Sepay-Signature")
	}))
	defer srv.Close()

	fwd, err := forward.NewForwarder(srv.URL, false)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	payload := []byte(`{"type":"payment_in","transferAmount":100000}`)
	headers := http.Header{"X-Sepay-Signature": {"sig-abc123"}}
	if _, err := fwd.Send(context.Background(), payload, headers); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if string(gotBody) != string(payload) {
		t.Fatalf("forwarded body = %q, want %q", gotBody, payload)
	}
	if gotSig != "sig-abc123" {
		t.Fatalf("forwarded signature = %q, want sig-abc123", gotSig)
	}
}

func TestSend_ConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing listens now

	fwd, err := forward.NewForwarder(addr, false)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	if _, err := fwd.Send(context.Background(), []byte(`{}`), nil); err == nil {
		t.Fatal("Send err = nil, want a transport error")
	}
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
				if err == nil {
					t.Fatal("err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewForwarder: %v", err)
			}
			if fwd.Target() != tt.wantURL {
				t.Fatalf("Target() = %q, want %q", fwd.Target(), tt.wantURL)
			}
			if fwd.IsLoopback() != tt.loopback {
				t.Fatalf("IsLoopback() = %v, want %v", fwd.IsLoopback(), tt.loopback)
			}
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
			if err != nil {
				t.Fatalf("NewForwarder: %v", err)
			}
			if fwd.IsPlaintextRemote() != tt.want {
				t.Fatalf("IsPlaintextRemote() = %v, want %v", fwd.IsPlaintextRemote(), tt.want)
			}
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
			fwd, err := forward.NewForwarder(tt.addr, true)
			if tt.allowed {
				if err != nil {
					t.Fatalf("NewForwarder: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewForwarder(%q, skip) = %v, want an error", tt.addr, fwd.Target())
			}
			if !strings.Contains(err.Error(), "--skip-tls-verify") {
				t.Fatalf("error %q does not name the flag", err)
			}
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
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	if _, err := strict.Send(context.Background(), []byte(`{}`), nil); err == nil {
		t.Fatal("a self-signed handler passed verification without --skip-tls-verify")
	}
	if strict.IsUnverifiedTLS() {
		t.Fatal("IsUnverifiedTLS() = true without the flag")
	}

	lax, err := forward.NewForwarder(srv.URL, true)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	res, err := lax.Send(context.Background(), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.StatusCode != http.StatusOK || !lax.IsUnverifiedTLS() {
		t.Fatalf("status = %d, IsUnverifiedTLS = %v", res.StatusCode, lax.IsUnverifiedTLS())
	}
}

func TestForwarder_OriginCollapsesTheSpellingsOfOneHandler(t *testing.T) {
	origin := func(addr string) string {
		t.Helper()
		f, err := forward.NewForwarder(addr, false)
		if err != nil {
			t.Fatalf("NewForwarder(%q): %v", addr, err)
		}
		return f.Origin()
	}

	same := []string{
		"localhost:3000", "http://localhost:3000", "http://localhost:3000/",
		"127.0.0.1:3000", "http://127.0.0.1:3000/", "LOCALHOST:3000", "[::1]:3000",
	}
	for _, pair := range [][2]string{{"localhost:80", "http://localhost"}, {"https://localhost:443/", "https://localhost/"}} {
		if origin(pair[0]) != origin(pair[1]) {
			t.Errorf("origin(%q)=%q and origin(%q)=%q name the same handler", pair[0], origin(pair[0]), pair[1], origin(pair[1]))
		}
	}
	want := origin(same[0])
	for _, addr := range same[1:] {
		if got := origin(addr); got != want {
			t.Errorf("origin(%q) = %q, want %q: it names the same handler", addr, got, want)
		}
	}

	for _, addr := range []string{
		"localhost:4000", "localhost:3000/hooks", "https://localhost:3000/", "example.test:3000",
	} {
		if got := origin(addr); got == want {
			t.Errorf("origin(%q) = %q, but it is a different handler", addr, got)
		}
	}
}
