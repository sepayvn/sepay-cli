// Package sse subscribes to the SePay webhook hub over Server-Sent Events.
package sse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
)

const (
	// subscribeTimeout bounds the wait for the hub's response headers; the stream itself has no deadline.
	subscribeTimeout = 30 * time.Second

	defaultHeartbeat = 15 * time.Second
	heartbeatGrace   = 2

	// maxHeartbeat bounds the server-reported cadence that sizes the dead-connection watchdog.
	maxHeartbeat = 5 * time.Minute

	// maxEventBytes caps one line and one event's accumulated data.
	maxEventBytes = 1 << 20
)

// ErrEventTooLarge means one event exceeded maxEventBytes.
var ErrEventTooLarge = errors.New("sse event exceeded size cap")

// ErrHeartbeatTimeout means no frame arrived within the watchdog window.
var ErrHeartbeatTimeout = errors.New("no heartbeat from hub")

// RetryableError is a transient subscribe failure (429 or 503) to retry after backoff.
type RetryableError struct {
	StatusCode int
	RetryAfter string
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("subscribe failed (%d), retryable", e.StatusCode)
}

// KeyRefusedError is the hub's 401: the listen_key is bad, not the login token,
// so it must not map to the "run sepay login" exit code.
type KeyRefusedError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *KeyRefusedError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("hub refused the listen key (%d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("hub refused the listen key (%d)", e.StatusCode)
}

// Event is one named SSE frame; heartbeats never surface.
type Event struct {
	Name string // "ready" | "webhook"
	Data []byte // raw JSON from the data: line(s)
}

// Client subscribes to the hub with no overall timeout and no redirects (SSRF guard).
type Client struct {
	httpClient *http.Client
}

// NewClient returns an SSE client ready to subscribe to a hub.
func NewClient() *Client {
	return newClient(subscribeTimeout)
}

func newClient(headerTimeout time.Duration) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = headerTimeout
	return &Client{
		httpClient: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: api.DebugTransport(tr),
		},
	}
}

// clampHeartbeat uses the default outside (0, maxHeartbeat], so the hub cannot disable or invert the watchdog.
func clampHeartbeat(heartbeat time.Duration) time.Duration {
	if heartbeat <= 0 || heartbeat > maxHeartbeat {
		return defaultHeartbeat
	}
	return heartbeat
}

// Open subscribes to hubURL and returns a Stream. Errors: 401 *KeyRefusedError,
// 403 *api.HTTPError, 429/503 *RetryableError.
func (c *Client) Open(ctx context.Context, hubURL, listenKey string, heartbeat time.Duration) (*Stream, error) {
	heartbeat = clampHeartbeat(heartbeat)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(hubURL, "/")+"/subscribe", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+listenKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, api.NewTransportError(req.URL.Host, c.httpClient.Timeout, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, statusError(resp)
	}

	s := &Stream{
		body:      resp.Body,
		frames:    make(chan frame, 16),
		done:      make(chan struct{}),
		heartbeat: heartbeat,
	}
	go s.readLoop()
	return s, nil
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, api.MaxResponseBytes))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		code, msg := decodeErr(body)
		return &KeyRefusedError{StatusCode: resp.StatusCode, Code: code, Message: msg}
	case http.StatusForbidden:
		// The hub never sends 403, so one comes from a policy proxy. Fatal.
		code, msg := decodeErr(body)
		return &api.HTTPError{StatusCode: resp.StatusCode, Code: code, Message: msg, RequestID: resp.Header.Get("X-Request-Id")}
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return &RetryableError{StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	default:
		return &api.HTTPError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body)), RequestID: resp.Header.Get("X-Request-Id")}
	}
}

func decodeErr(body []byte) (code, message string) {
	var env api.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", strings.TrimSpace(string(body))
	}
	return env.ErrorCode, env.Message
}

// Stream is a live subscription; call Close to release it.
type Stream struct {
	body      io.ReadCloser
	frames    chan frame
	done      chan struct{}
	closeOnce sync.Once
	heartbeat time.Duration

	readErr error // set by readLoop before it closes frames
}

type frame struct {
	name    string
	data    []byte
	comment bool // heartbeat / comment line, carries no event
}

// Read returns the next named event, ErrHeartbeatTimeout, ctx.Err(), or the stream's end error.
func (s *Stream) Read(ctx context.Context) (Event, error) {
	timer := time.NewTimer(heartbeatGrace * s.heartbeat)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-timer.C:
			return Event{}, ErrHeartbeatTimeout
		case fr, ok := <-s.frames:
			if !ok {
				if s.readErr != nil {
					return Event{}, s.readErr
				}
				return Event{}, io.EOF
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(heartbeatGrace * s.heartbeat)
			if fr.comment {
				continue
			}
			return Event{Name: fr.name, Data: fr.data}, nil
		}
	}
}

// Close releases the connection.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return s.body.Close()
}

func (s *Stream) readLoop() {
	defer close(s.frames)

	sc := bufio.NewScanner(s.body)
	sc.Buffer(make([]byte, 0, 64*1024), maxEventBytes)

	var name string
	var data []byte
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if name != "" || len(data) > 0 {
				if !s.emit(frame{name: name, data: data}) {
					return
				}
				name, data = "", nil
			}
		case line[0] == ':':
			if !s.emit(frame{comment: true}) {
				return
			}
		case bytes.HasPrefix(line, []byte("event:")):
			name = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			v := line[len("data:"):]
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			// Bound the whole event: Scanner.Buffer caps only one line.
			if len(data)+len(v) > maxEventBytes {
				s.readErr = ErrEventTooLarge
				return
			}
			data = append(data, v...)
		}
	}
	s.readErr = sc.Err()
}

func (s *Stream) emit(fr frame) bool {
	select {
	case s.frames <- fr:
		return true
	case <-s.done:
		return false
	}
}
