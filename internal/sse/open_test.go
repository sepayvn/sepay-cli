package sse

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_HubThatNeverAnswersTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	start := time.Now()
	_, err = newClient(200*time.Millisecond).Open(context.Background(), "http://"+ln.Addr().String(), "lk_test", time.Second)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestOpen_HeaderTimeoutDoesNotCutTheStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte("event: ready\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	stream, err := newClient(100*time.Millisecond).Open(context.Background(), srv.URL, "lk_test", time.Second)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	ev, err := stream.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ready", ev.Name)
}
