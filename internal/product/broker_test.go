package product

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func brokerServing(t *testing.T, status int, body string) *Broker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/products/pg/token", r.URL.Path)
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewBroker(api.NewClient(srv.URL, "tok"))
}

func TestBrokerMint_Basic(t *testing.T) {
	b := brokerServing(t, http.StatusOK,
		`{"status":"success","data":{"auth_type":"basic","merchant_id":"SP-TEST-1","secret_key":"spsk_test_x"}}`)

	c, err := b.Mint(context.Background(), PG, "sandbox")
	require.NoError(t, err)
	assert.Equal(t, authBasic, c.AuthType)
	id, secret := c.BasicAuth()
	assert.Equal(t, "SP-TEST-1", id)
	assert.Equal(t, "spsk_test_x", secret)
	assert.False(t, c.Expired())
	assert.NotContains(t, c.String(), "spsk_test_x")
}

func TestBrokerMint_Bearer(t *testing.T) {
	exp := "2999-01-01T00:00:00Z"
	b := brokerServing(t, http.StatusOK,
		`{"status":"success","data":{"auth_type":"bearer","access_token":"at_x","token_type":"Bearer","expires_at":"`+exp+`"}}`)

	c, err := b.Mint(context.Background(), PG, "sandbox")
	require.NoError(t, err)
	assert.Equal(t, authBearer, c.AuthType)
	assert.Equal(t, "at_x", c.AccessToken)
	assert.False(t, c.Expired())
	want, _ := time.Parse(time.RFC3339, exp)
	assert.True(t, c.ExpiresAt.Equal(want))
}

func TestBrokerMint_SandboxOnly404(t *testing.T) {
	b := brokerServing(t, http.StatusNotFound,
		`{"status":"error","error_code":"not_found","message":"This endpoint is only available in the sandbox environment"}`)

	_, err := b.Mint(context.Background(), PG, "production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox-only")
}

func TestBrokerMint_RejectsIncompleteAndUnknown(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"basic missing secret", `{"status":"success","data":{"auth_type":"basic","merchant_id":"SP-1"}}`},
		{"bearer missing token", `{"status":"success","data":{"auth_type":"bearer","expires_at":"2999-01-01T00:00:00Z"}}`},
		{"bad expires_at", `{"status":"success","data":{"auth_type":"bearer","access_token":"at_x","expires_at":"nonsense"}}`},
		{"unknown auth_type", `{"status":"success","data":{"auth_type":"magic"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := brokerServing(t, http.StatusOK, tt.body)
			_, err := b.Mint(context.Background(), PG, "sandbox")
			require.Error(t, err)
		})
	}
}
