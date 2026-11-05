package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDotSegment(t *testing.T) {
	cases := map[string]bool{
		"/webhooks/abc":            false,
		"/webhooks/..":             true,
		"/webhooks/.":              true,
		"/bank-accounts/x/va/..":   true,
		"/webhooks/%2e%2e":         true,
		"/webhooks/%2E.":           true,
		"/webhooks/..x":            false,
		"/webhooks/abc?next=../..": false,
		"/x/%zz/..":                true,
	}
	for path, want := range cases {
		assert.Equal(t, want, HasDotSegment(path), path)
	}
}

// The last line of defence: a path that reaches the client with a dot segment is
// never sent, whichever command built it.
func TestClient_RefusesDotSegmentWithoutSending(t *testing.T) {
	sent := 0
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { sent++ }))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/v2", "tok")
	ctx := context.Background()

	_, err := c.Delete(ctx, "/bank-accounts/x/va/..", WriteOptions{})
	require.ErrorIs(t, err, ErrDotSegment)
	_, err = c.Get(ctx, "/webhooks/%2e%2e", nil)
	require.ErrorIs(t, err, ErrDotSegment)
	_, err = c.Request(ctx, http.MethodPost, "/x/../oauth/tokens", nil, WriteOptions{})
	require.ErrorIs(t, err, ErrDotSegment)
	assert.Zero(t, sent)
}
