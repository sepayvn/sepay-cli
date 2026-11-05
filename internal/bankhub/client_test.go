package bankhub

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

func TestClient_GetParsesDataAndMeta(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"xid":"co_1"}],"meta":{"total":3,"per_page":20,"page_count":1,"has_more":false}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/company", nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Meta)
	assert.Equal(t, 3, resp.Meta.Total)
	assert.Equal(t, 1, resp.Meta.PageCount)
	items, _, err := ListInto[map[string]any](resp)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "co_1", items[0]["xid"])
}

func TestClient_DataIntoDetail(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"xid":"co_1","full_name":"ACME"}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/company/co_1", nil)
	require.NoError(t, err)
	co, err := DataInto[struct {
		XID      string `json:"xid"`
		FullName string `json:"full_name"`
	}](resp)
	require.NoError(t, err)
	assert.Equal(t, "co_1", co.XID)
	assert.Equal(t, "ACME", co.FullName)
}

func TestClient_IntoUnwrapsFlatLinkToken(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"xid":"lt_1","purpose":"LINK_BANK_ACCOUNT","status":"ACTIVE"}`))
	})
	resp, err := c.Post(context.Background(), "/v1/link-token/get", map[string]string{"link_token": "x"})
	require.NoError(t, err)
	var lt struct {
		XID     string `json:"xid"`
		Purpose string `json:"purpose"`
	}
	require.NoError(t, resp.Into(&lt))
	assert.Equal(t, "lt_1", lt.XID)
	assert.Equal(t, "LINK_BANK_ACCOUNT", lt.Purpose)
}

func TestClient_SendsBearerAuth(t *testing.T) {
	var gotAuth string
	c, _ := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	_, err := c.Get(context.Background(), "/v1/company", nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok_1", gotAuth)
}

func TestClient_ReMintsOnceOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			assert.Equal(t, "Bearer tok_1", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"Unauthenticated"}`))
			return
		}
		assert.Equal(t, "Bearer tok_new", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":{"xid":"co_1"}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/company", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"xid":"co_1"}`, string(resp.Data))
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClient_Persistent401MapsToAuthExit(t *testing.T) {
	c, creds := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"Unauthenticated"}`))
	})
	_, err := c.Get(context.Background(), "/v1/company", nil)
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes), "re-mints at most once")
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 401, he.StatusCode) // cmdutil maps 401 → ExitAuth (tested there)
	assert.Contains(t, he.Message, "Unauthenticated")
}

func TestClient_ErrorShapeStandardWithFields(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":422,"message":"Không hợp lệ","errors":{"transfer_type":"Ngân hàng này chỉ hỗ trợ tiền vào"}}`))
	})
	_, err := c.Post(context.Background(), "/v1/transaction/create", map[string]string{})
	require.Error(t, err)
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 422, he.StatusCode)
	assert.Equal(t, "422", he.Code)
	assert.Contains(t, he.Message, "Không hợp lệ")
	assert.Equal(t, "Ngân hàng này chỉ hỗ trợ tiền vào", he.Errors["transfer_type"])
}

func TestClient_ErrorShapeFrameworkDefault(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"error":400,"messages":{"error":"Không có thông tin cập nhật"}}`))
	})
	_, err := c.Post(context.Background(), "/v1/company/edit/x", map[string]string{})
	require.Error(t, err)
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 400, he.StatusCode)
	assert.Contains(t, he.Message, "Không có thông tin cập nhật")
}

func TestClient_ErrorShapeStringCode(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"RESOURCE_NOT_FOUND","message":"Không tìm thấy tài nguyên"}`))
	})
	_, err := c.Post(context.Background(), "/v1/link-token/get", map[string]string{"link_token": "x"})
	require.Error(t, err)
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 404, he.StatusCode)
	assert.Equal(t, "RESOURCE_NOT_FOUND", he.Code)
	assert.Contains(t, he.Message, "Không tìm thấy")
}

func TestClient_ServerErrorDropsEchoedStatusCode(t *testing.T) {
	for _, tc := range []struct {
		body, wantCode string
	}{
		{`{"code":500,"message":"Lỗi hệ thống"}`, ""},
		{`{"code":"UPSTREAM_TIMEOUT","message":"Ngân hàng không phản hồi"}`, "UPSTREAM_TIMEOUT"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.Post(context.Background(), "/v1/transaction/create", map[string]string{})
			var he *api.HTTPError
			require.ErrorAs(t, err, &he)
			assert.Equal(t, 500, he.StatusCode)
			assert.Equal(t, tc.wantCode, he.Code)
		})
	}
}

func TestClient_RetriesOn429ForGet(t *testing.T) {
	var calls int32
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	_, err := c.Get(context.Background(), "/v1/company", nil)
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
	_, err := c.Post(context.Background(), "/v1/link-token/get", map[string]string{"link_token": "x"})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestClient_MutationIsNotReplayedOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"Unauthenticated"}`))
	})
	_, err := c.Post(context.Background(), "/v1/company/create", map[string]string{"full_name": "ACME"})
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
			_, _ = w.Write([]byte(`{"code":401,"message":"Unauthenticated"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":201,"message":"ok"}`))
	})
	_, err := c.Get(context.Background(), "/v1/company/create", nil)
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
