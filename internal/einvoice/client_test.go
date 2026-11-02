package einvoice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCreds struct {
	token     string
	refreshes int32
}

func (f *fakeCreds) Get(context.Context, string) (*product.Credential, error) {
	return &product.Credential{AuthType: "bearer", AccessToken: f.token, TokenType: "Bearer"}, nil
}

func (f *fakeCreds) Refresh(context.Context, string) (*product.Credential, error) {
	atomic.AddInt32(&f.refreshes, 1)
	f.token = "tok_new"
	return &product.Credential{AuthType: "bearer", AccessToken: f.token, TokenType: "Bearer"}, nil
}

func clientTo(t *testing.T, h http.HandlerFunc) (*Client, *fakeCreds) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	creds := &fakeCreds{token: "tok_1"}
	c := NewClient(srv.URL, creds)
	c.MaxRetries = 2
	return c, creds
}

func TestClient_DataIntoDetail(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"acc_1","provider":"matbao"}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/provider-accounts/acc_1", nil)
	require.NoError(t, err)
	acc, err := DataInto[struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}](resp)
	require.NoError(t, err)
	assert.Equal(t, "acc_1", acc.ID)
	assert.Equal(t, "matbao", acc.Provider)
}

func TestClient_DataIntoEmptyErrors(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`)) // no "data"
	})
	resp, err := c.Get(context.Background(), "/v1/provider-accounts/acc_1", nil)
	require.NoError(t, err)
	_, err = DataInto[map[string]any](resp)
	require.Error(t, err)
}

func TestClient_ListIntoPagingAndItems(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"paging":{"total":3,"per_page":20,"current_page":1,"page_count":1,"has_more":false},"items":[{"id":"acc_1"}]}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/provider-accounts", nil)
	require.NoError(t, err)
	items, paging, err := ListInto[map[string]any](resp)
	require.NoError(t, err)
	require.NotNil(t, paging)
	assert.Equal(t, 3, paging.Total)
	require.Len(t, items, 1)
	assert.Equal(t, "acc_1", items[0]["id"])
}

func TestClient_ListIntoEmptyIsNonNil(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"paging":{"total":0},"items":[]}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/invoices", nil)
	require.NoError(t, err)
	items, _, err := ListInto[map[string]any](resp)
	require.NoError(t, err)
	assert.NotNil(t, items)
	assert.Len(t, items, 0)
}

func TestClient_SendsBearerAuth(t *testing.T) {
	var gotAuth string
	c, _ := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	_, err := c.Get(context.Background(), "/v1/usage", nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok_1", gotAuth)
}

func TestClient_ReMintsOnceOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			assert.Equal(t, "Bearer tok_1", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":"TOKEN_EXPIRED","message":"expired"}}`))
			return
		}
		assert.Equal(t, "Bearer tok_new", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":{"quota_remaining":999}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/usage", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"quota_remaining":999}`, string(resp.Data))
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClient_Persistent401MapsToAuthExit(t *testing.T) {
	c, creds := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED","message":"Access token is invalid"}}`))
	})
	_, err := c.Get(context.Background(), "/v1/usage", nil)
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes), "re-mints at most once")
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 401, he.StatusCode) // cmdutil maps 401 → ExitAuth (tested there)
	assert.Equal(t, "UNAUTHORIZED", he.Code)
	assert.Contains(t, he.Message, "Access token is invalid")
}

func TestClient_ErrorShapeWithDetails(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"VALIDATION_ERROR","message":"Dữ liệu không hợp lệ","details":{"invoice_series":"Sai định dạng"}}}`))
	})
	_, err := c.Post(context.Background(), "/v1/invoices/create", map[string]string{})
	require.Error(t, err)
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 400, he.StatusCode)
	assert.Equal(t, "VALIDATION_ERROR", he.Code)
	assert.Contains(t, he.Message, "Dữ liệu không hợp lệ")
	assert.Equal(t, "Sai định dạng", he.Errors["invoice_series"])
}

func TestClient_ErrorFallsBackToStatusText(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	_, err := c.Get(context.Background(), "/v1/usage", nil)
	require.Error(t, err)
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 405, he.StatusCode)
	assert.NotEmpty(t, he.Message)
}

func TestClient_RetriesOn429ForGet(t *testing.T) {
	var calls int32
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"paging":{"total":0},"items":[]}}`))
	})
	_, err := c.Get(context.Background(), "/v1/invoices", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClient_DoesNotRetryPostOn429(t *testing.T) {
	var calls int32
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Post(context.Background(), "/v1/invoices/create", map[string]string{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestClient_MutationIsNotReplayedOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED"}}`))
	})
	_, err := c.Post(context.Background(), "/v1/invoices/issue", map[string]string{"reference_code": "REF-1"})
	require.Error(t, err)

	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 401, he.StatusCode, "the caller sees the 401, not a swallowed retry")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "the mutation reached the server exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes), "the rejected credential was still replaced")
}

func TestClient_ReadIsStillReplayedOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":"UNAUTHORIZED"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	_, err := c.Get(context.Background(), "/v1/invoices/issue", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes))
}

func TestClient_RefusesDotSegmentWithoutSending(t *testing.T) {
	sent := 0
	c, _ := clientTo(t, func(http.ResponseWriter, *http.Request) { sent++ })
	ctx := context.Background()

	_, err := c.Get(ctx, "/v1/company/..", nil)
	require.ErrorIs(t, err, api.ErrDotSegment)
	_, err = c.Post(ctx, "/v1/x/%2e%2e/edit", nil)
	require.ErrorIs(t, err, api.ErrDotSegment)
	assert.Zero(t, sent)
}
