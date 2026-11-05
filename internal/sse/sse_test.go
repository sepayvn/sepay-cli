package sse_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/sse"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStream_EventTooLarge(t *testing.T) {
	big := strings.Repeat("x", 600*1024) // each line < 1 MiB cap, two sum > 1 MiB
	raw := "event: webhook\ndata: " + big + "\ndata: " + big + "\n\n"
	srv := httptest.NewServer(streamHandler(raw, false))
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Second)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	_, err = stream.Read(context.Background())
	assert.ErrorIs(t, err, sse.ErrEventTooLarge)
}

// streamHandler writes raw SSE text, then optionally holds the connection until the client leaves.
func streamHandler(raw string, hold bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if raw != "" {
			_, _ = io.WriteString(w, raw)
		}
		w.(http.Flusher).Flush()
		if hold {
			<-r.Context().Done()
		}
	}
}

func TestStream_ReadsEventsThenEOF(t *testing.T) {
	raw := "event: ready\ndata: {\"environment\":\"sandbox\"}\n\n" +
		"event: webhook\ndata: {\"type\":\"payment_in\"}\n\n"
	srv := httptest.NewServer(streamHandler(raw, false))
	defer srv.Close()

	c := sse.NewClient()
	stream, err := c.Open(context.Background(), srv.URL, "lk_test", time.Second)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ready, err := stream.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Name)
	hook, err := stream.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "webhook", hook.Name)
	assert.Equal(t, `{"type":"payment_in"}`, string(hook.Data)) //nolint:testifylint // data passes through byte for byte
	_, err = stream.Read(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestStream_SkipsHeartbeat(t *testing.T) {
	raw := ": heartbeat\n\nevent: webhook\ndata: {}\n\n"
	srv := httptest.NewServer(streamHandler(raw, false))
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Second)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ev, err := stream.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "webhook", ev.Name, "heartbeat skipped")
}

func TestStream_HeartbeatTimeout(t *testing.T) {
	srv := httptest.NewServer(streamHandler("", true)) // connect, then silence
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", 20*time.Millisecond)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = stream.Read(ctx)
	assert.ErrorIs(t, err, sse.ErrHeartbeatTimeout)
}

func TestOpen_StatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"status":"error","error_code":"unauthorized"}`, ""},
		{"forbidden", http.StatusForbidden, `{"status":"error","error_code":"insufficient_scope"}`, ""},
		{"rate limited", http.StatusTooManyRequests, "", "3"},
		{"unavailable", http.StatusServiceUnavailable, "", "5"},
		{"bad gateway", http.StatusBadGateway, "<html>502</html>", ""},
		{"gateway timeout", http.StatusGatewayTimeout, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			_, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Second)
			require.Error(t, err)

			switch tt.status {
			case http.StatusUnauthorized:
				var kr *sse.KeyRefusedError
				require.ErrorAs(t, err, &kr)
				assert.Equal(t, tt.status, kr.StatusCode)
			case http.StatusForbidden:
				var he *api.HTTPError
				require.ErrorAs(t, err, &he)
				assert.Equal(t, tt.status, he.StatusCode)
			default:
				var re *sse.RetryableError
				require.ErrorAs(t, err, &re)
				assert.Equal(t, tt.status, re.StatusCode)
				assert.Equal(t, tt.retryAfter, re.RetryAfter)
			}
		})
	}
}

func TestStream_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(streamHandler("", true))
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Minute)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = stream.Read(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}
