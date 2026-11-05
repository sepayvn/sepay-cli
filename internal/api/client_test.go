package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestReadBody_ReadErrorNotSwallowed(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(errReader{})}
	_, err := ReadBody(resp)
	require.Error(t, err)
	var te *TransportError
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "the connection to SePay was cut off", te.Reason())
}

// The server answered, only too much: a resend would fetch the same body, and the
// failure is not the network's.
func TestGet_OversizedResponseIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write(bytes.Repeat([]byte("x"), MaxResponseBytes+1))
	}))
	defer srv.Close()

	var logged []string
	c := NewClient(srv.URL, "tok")
	c.MaxRetries = 3
	c.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	_, err := c.Get(context.Background(), "/transactions", nil)
	require.ErrorIs(t, err, ErrResponseTooLarge)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	for _, line := range logged {
		assert.NotContains(t, line, "cannot reach")
	}
}

func TestPost_SendsBodyAndIdempotencyKey(t *testing.T) {
	var gotKey, gotBody, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Idempotency-Key")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	env, err := c.Post(context.Background(), "/webhooks", WriteOptions{
		Body:           map[string]string{"url": "https://x.test"},
		IdempotencyKey: "key-123",
	})
	require.NoError(t, err)
	assert.Equal(t, "key-123", gotKey)
	assert.Equal(t, "application/json", gotContentType)
	assert.JSONEq(t, `{"url":"https://x.test"}`, gotBody)
	out, err := DataInto[struct {
		ID string `json:"id"`
	}](env)
	require.NoError(t, err)
	assert.Equal(t, "wh_1", out.ID)
}

func TestPost_RetriesReuseSameKey(t *testing.T) {
	var calls int32
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("X-Idempotency-Key"))
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"rate_limited"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	c.MaxRetries = 2
	_, err := c.Post(context.Background(), "/webhooks", WriteOptions{IdempotencyKey: "same-key"})
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, "same-key", keys[0])
	assert.Equal(t, keys[0], keys[1], "retry must reuse the idempotency key")
}

func TestPost_WithoutKeyDoesNotRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"rate_limited"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	c.MaxRetries = 3
	_, err := c.Post(context.Background(), "/transactions/simulate", WriteOptions{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "POST without a key must not be retried")
}

func TestPutDelete_RetryOn429(t *testing.T) {
	for _, verb := range []string{"PUT", "DELETE"} {
		t.Run(verb, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if atomic.AddInt32(&calls, 1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"status":"error"}`))
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "tok")
			c.MaxRetries = 2
			var err error
			if verb == "PUT" {
				_, err = c.Put(context.Background(), "/webhooks/wh_1", WriteOptions{Body: map[string]string{"name": "x"}})
			} else {
				_, err = c.Delete(context.Background(), "/webhooks/wh_1", WriteOptions{})
			}
			require.NoError(t, err)
			assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
		})
	}
}

func TestDelete_NoContentIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	env, err := c.Delete(context.Background(), "/webhooks/wh_1", WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "success", env.Status)
}

func TestRedirectNotFollowedIsError(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Location", "https://elsewhere.example/api")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.Post(context.Background(), "/webhooks", WriteOptions{IdempotencyKey: "k"})
	var httpErr *HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusFound, httpErr.StatusCode)
	assert.Equal(t, 1, hits, "redirect must not be followed")
}

func TestPost_ErrorMapsToHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"validation_error","message":"bad"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.Post(context.Background(), "/webhooks", WriteOptions{IdempotencyKey: "k"})
	var httpErr *HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, 422, httpErr.StatusCode)
	assert.Equal(t, "validation_error", httpErr.Code)
}

func TestClient_NonJSONErrorKeepsItsStatus(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusUnauthorized, http.StatusNotImplemented} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "<!doctype html><html><body>nope</body></html>")
		}))
		c := NewClient(srv.URL, "tok")
		c.MaxRetries = 0
		_, err := c.Get(context.Background(), "/x", nil)
		srv.Close()

		require.Error(t, err, status)
		var he *HTTPError
		require.ErrorAs(t, err, &he, status)
		assert.Equal(t, status, he.StatusCode)
		assert.Equal(t, http.StatusText(status), he.Message)
		assert.NotContains(t, err.Error(), "invalid character", status)
	}
}

func TestClient_NonJSONSuccessNamesTheHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<!doctype html><html><body>hello</body></html>")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.Get(context.Background(), "/x", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-JSON response")
	assert.Contains(t, err.Error(), srv.URL)
	assert.NotContains(t, err.Error(), "invalid character")
}

func TestNewIdempotencyKey_UniqueHex(t *testing.T) {
	a, err := NewIdempotencyKey()
	require.NoError(t, err)
	b, err := NewIdempotencyKey()
	require.NoError(t, err)
	assert.Len(t, a, 32)
	assert.NotEqual(t, a, b)
}

func TestClient_ExhaustedQuotaIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"quota_exceeded","message":"day limit"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.Post(context.Background(), "/transactions/simulate", WriteOptions{IdempotencyKey: "k1"})

	require.Error(t, err)
	assert.Equal(t, 1, calls, "a spent quota must not be retried")
}

func TestClient_RateLimitIsStillRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"rate_limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.Post(context.Background(), "/x", WriteOptions{IdempotencyKey: "k1"})

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestBackoff_CapsWithoutOverflow(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter string
		attempt    int
		want       time.Duration
	}{
		{"first attempt", "", 0, 1 * time.Second},
		{"doubles", "", 5, 32 * time.Second},
		{"caps at 60s", "", 6, maxBackoff},
		{"last attempt that fit in int64", "", 33, maxBackoff},
		{"attempt that used to wrap negative", "", 34, maxBackoff},
		{"attempt that used to land on zero", "", 55, maxBackoff},
		{"attempt past the shift width", "", 200, maxBackoff},
		{"negative attempt cannot panic the shift", "", -1, 1 * time.Second},
		{"retry-after is honoured", "5", 0, 5 * time.Second},
		{"retry-after is capped", "120", 0, maxBackoff},
		{"largest retry-after that fit in int64", "9223372036", 0, maxBackoff},
		{"retry-after that used to wrap negative", "9223372037", 0, maxBackoff},
		{"retry-after at int64 max", "9223372036854775807", 0, maxBackoff},
		{"non-positive retry-after falls back to attempt", "-3", 2, 4 * time.Second},
		{"unparseable retry-after falls back to attempt", "Wed, 21 Oct 2015 07:28:00 GMT", 2, 4 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Backoff(tc.retryAfter, tc.attempt)
			assert.Equal(t, tc.want, got)
			assert.Greater(t, got, time.Duration(0), "a non-positive wait retries with no delay")
			assert.LessOrEqual(t, got, maxBackoff)
		})
	}
}

func unauthorizedUnless(good string, calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	}))
}

func TestClient_UnauthorizedRenewsTheTokenAndReplays(t *testing.T) {
	// A keyless POST too: the 401 comes before any handler runs.
	for _, verb := range []string{"GET", "POST"} {
		t.Run(verb, func(t *testing.T) {
			var calls int32
			srv := unauthorizedUnless("fresh", &calls)
			defer srv.Close()

			c := NewClient(srv.URL, "revoked")
			var asked []string
			c.Reauth = func(_ context.Context, rejected string) string {
				asked = append(asked, rejected)
				return "fresh"
			}
			_, err := c.Request(context.Background(), verb, "/webhooks", nil, WriteOptions{})
			require.NoError(t, err)
			assert.Equal(t, []string{"revoked"}, asked)
			assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

			_, err = c.Get(context.Background(), "/webhooks", nil)
			require.NoError(t, err)
			assert.Len(t, asked, 1)
		})
	}
}

func TestClient_UnauthorizedWithoutRenewalReportsTheError(t *testing.T) {
	cases := []struct {
		name   string
		reauth func(context.Context, string) string
	}{
		{"no hook", nil},
		{"no replacement", func(context.Context, string) string { return "" }},
		{"same token back", func(_ context.Context, rejected string) string { return rejected }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv := unauthorizedUnless("fresh", &calls)
			defer srv.Close()

			c := NewClient(srv.URL, "revoked")
			c.Reauth = tc.reauth
			_, err := c.Get(context.Background(), "/webhooks", nil)
			var he *HTTPError
			require.ErrorAs(t, err, &he)
			assert.Equal(t, http.StatusUnauthorized, he.StatusCode)
			assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
		})
	}
}

func TestClient_RenewsAtMostOncePerClient(t *testing.T) {
	var calls int32
	srv := unauthorizedUnless("never", &calls)
	defer srv.Close()

	c := NewClient(srv.URL, "revoked")
	var n int
	c.Reauth = func(context.Context, string) string { n++; return "fresh" }
	for range 2 {
		_, err := c.Get(context.Background(), "/webhooks", nil)
		require.Error(t, err)
	}
	assert.Equal(t, 1, n)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
}

func TestWithRequestID(t *testing.T) {
	h := http.Header{"X-Request-Id": {"req_9"}}
	err := WithRequestID(&HTTPError{StatusCode: 500}, h)
	var he *HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "req_9", he.RequestID)

	kept := WithRequestID(&HTTPError{StatusCode: 500, RequestID: "own"}, h)
	require.ErrorAs(t, kept, &he)
	assert.Equal(t, "own", he.RequestID)

	plain := errors.New("dial tcp")
	assert.Equal(t, plain, WithRequestID(plain, h))
}
