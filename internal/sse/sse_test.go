package sse_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/sse"
)

func TestStream_EventTooLarge(t *testing.T) {
	big := strings.Repeat("x", 600*1024) // each line < 1 MiB cap, two sum > 1 MiB
	raw := "event: webhook\ndata: " + big + "\ndata: " + big + "\n\n"
	srv := httptest.NewServer(streamHandler(raw, false))
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if _, err := stream.Read(context.Background()); !errors.Is(err, sse.ErrEventTooLarge) {
		t.Fatalf("Read err = %v, want ErrEventTooLarge", err)
	}
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
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ready, err := stream.Read(ctx)
	if err != nil || ready.Name != "ready" {
		t.Fatalf("first read = (%q, %v), want ready", ready.Name, err)
	}
	hook, err := stream.Read(ctx)
	if err != nil || hook.Name != "webhook" {
		t.Fatalf("second read = (%q, %v), want webhook", hook.Name, err)
	}
	if string(hook.Data) != `{"type":"payment_in"}` {
		t.Fatalf("webhook data = %q", hook.Data)
	}
	if _, err := stream.Read(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("third read err = %v, want io.EOF", err)
	}
}

func TestStream_SkipsHeartbeat(t *testing.T) {
	raw := ": heartbeat\n\nevent: webhook\ndata: {}\n\n"
	srv := httptest.NewServer(streamHandler(raw, false))
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ev, err := stream.Read(ctx)
	if err != nil || ev.Name != "webhook" {
		t.Fatalf("read = (%q, %v), want webhook (heartbeat skipped)", ev.Name, err)
	}
}

func TestStream_HeartbeatTimeout(t *testing.T) {
	srv := httptest.NewServer(streamHandler("", true)) // connect, then silence
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := stream.Read(ctx); !errors.Is(err, sse.ErrHeartbeatTimeout) {
		t.Fatalf("read err = %v, want ErrHeartbeatTimeout", err)
	}
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
			if err == nil {
				t.Fatal("Open err = nil, want error")
			}

			switch tt.status {
			case http.StatusUnauthorized:
				var kr *sse.KeyRefusedError
				if !errors.As(err, &kr) || kr.StatusCode != tt.status {
					t.Fatalf("err = %v, want *KeyRefusedError %d", err, tt.status)
				}
			case http.StatusForbidden:
				var he *api.HTTPError
				if !errors.As(err, &he) || he.StatusCode != tt.status {
					t.Fatalf("err = %v, want *api.HTTPError %d", err, tt.status)
				}
			default:
				var re *sse.RetryableError
				if !errors.As(err, &re) || re.StatusCode != tt.status || re.RetryAfter != tt.retryAfter {
					t.Fatalf("err = %v, want *RetryableError %d retry=%q", err, tt.status, tt.retryAfter)
				}
			}
		})
	}
}

func TestStream_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(streamHandler("", true))
	defer srv.Close()

	stream, err := sse.NewClient().Open(context.Background(), srv.URL, "lk_test", time.Minute)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("read err = %v, want context.Canceled", err)
	}
}
