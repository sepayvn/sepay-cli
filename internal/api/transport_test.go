package api

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransportError_Reason(t *testing.T) {
	const host = "cli-api-sandbox.sepay.vn"
	urlErr := func(err error) error { return &url.Error{Op: "Get", URL: "https://" + host + "/v2/x", Err: err} }
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"client timeout", urlErr(os.ErrDeadlineExceeded), host + " did not answer within 30s"},
		{"context deadline", urlErr(context.DeadlineExceeded), host + " did not answer within 30s"},
		{"dns", urlErr(&net.DNSError{Err: "no such host", Name: host, IsNotFound: true}), "cannot find " + host + " (DNS lookup failed)"},
		{"refused", urlErr(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), host + " refused the connection"},
		{"no route", urlErr(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}), "no network route to " + host},
		{"untrusted certificate", urlErr(x509.UnknownAuthorityError{}), "the secure connection to " + host + " failed: its certificate is not trusted"},
		{"reset", urlErr(os.NewSyscallError("read", syscall.ECONNRESET)), "the connection to " + host + " was cut off"},
		{"other", urlErr(errors.New("proxyconnect tcp: boom")), "cannot reach " + host},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := NewTransportError(host, 30*time.Second, tt.err)
			assert.Equal(t, tt.want, te.Reason())
			assert.Equal(t, tt.want+"; check your internet connection and retry", te.Error())
			assert.NotContains(t, te.Error(), "Get \"", "the Go error chain stays out of the message")
			var ue *url.Error
			assert.ErrorAs(t, te, &ue, "callers that branch on *url.Error still see it")
		})
	}
}

// An unreachable host must be retried with plain-word notes and end in a TransportError.
func TestGet_UnreachableHostNotesAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	c := NewClient(base+"/v2", "tok")
	c.MaxRetries = 1
	var notes []string
	c.Logf = func(format string, a ...any) { notes = append(notes, strings.TrimSpace(fmt.Sprintf(format, a...))) }

	_, err := c.Get(context.Background(), "/bank-accounts", nil)
	var te *TransportError
	require.ErrorAs(t, err, &te)
	host := strings.TrimPrefix(base, "http://")
	assert.Equal(t, host+" refused the connection; check your internet connection and retry", err.Error())
	require.Len(t, notes, 1)
	assert.Equal(t, "cannot reach SePay ("+host+" refused the connection), retrying in 1s (attempt 1 of 1)...", notes[0])
}

// The real http.Client timeout (the "Client.Timeout exceeded while awaiting headers" case).
func TestGet_ClientTimeoutNamesTheWait(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	c := NewClient(srv.URL+"/v2", "tok")
	c.MaxRetries = 0
	c.httpClient.Timeout = 50 * time.Millisecond

	_, err := c.Get(context.Background(), "/bank-accounts", nil)
	host := strings.TrimPrefix(srv.URL, "http://")
	assert.EqualError(t, err, host+" did not answer within 50ms; check your internet connection and retry")
}

// A conditional write whose outcome is unknown is not resent: if it landed, the repeat would get a
// 409 for a write that succeeded. A key lets the server replay instead; a 429 was never processed.
func TestPut_IfMatchIsNotResentWhenTheOutcomeIsUnknown(t *testing.T) {
	cases := []struct {
		name     string
		header   http.Header
		key      string
		status   int
		attempts int
	}{
		{"if-match, connection cut", http.Header{"If-Match": {"v1"}}, "", 0, 1},
		{"if-match with a key, connection cut", http.Header{"If-Match": {"v1"}}, "k-12345678", 0, 2},
		{"no if-match, connection cut", nil, "", 0, 2},
		{"if-match, rate limited", http.Header{"If-Match": {"v1"}}, "", http.StatusTooManyRequests, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				if tc.status == 0 {
					panic(http.ErrAbortHandler)
				}
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"status":"error","error_code":"rate_limited","message":"slow down"}`))
			}))
			defer srv.Close()

			c := NewClient(srv.URL+"/v2", "tok")
			c.MaxRetries = 1
			_, err := c.Put(context.Background(), "/company/paycode", WriteOptions{Body: map[string]string{"a": "b"}, Header: tc.header, IdempotencyKey: tc.key})
			require.Error(t, err)
			assert.Equal(t, tc.attempts, attempts)
		})
	}
}

// Ctrl-C stops a request; the message must not send the user to check a connection that is fine.
func TestTransportError_CancelledIsNotANetworkFailure(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	c := NewClient(srv.URL+"/v2", "tok")
	_, err := c.Get(ctx, "/bank-accounts", nil)

	var te *TransportError
	require.ErrorAs(t, err, &te)
	assert.True(t, te.Cancelled())
	assert.Equal(t, "the request to "+strings.TrimPrefix(srv.URL, "http://")+" was cancelled", err.Error())
	assert.NotContains(t, err.Error(), "internet connection")
	assert.ErrorIs(t, err, context.Canceled, "callers that check for cancellation still see it")
}
