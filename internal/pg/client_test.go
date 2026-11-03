package pg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCreds struct {
	id, secret string
	refreshes  int32
}

func (f *fakeCreds) Get(context.Context, string) (*product.Credential, error) {
	return &product.Credential{AuthType: "basic", MerchantID: f.id, SecretKey: f.secret}, nil
}

func (f *fakeCreds) Refresh(context.Context, string) (*product.Credential, error) {
	atomic.AddInt32(&f.refreshes, 1)
	f.id, f.secret = "SP-NEW", "spsk_new"
	return &product.Credential{AuthType: "basic", MerchantID: f.id, SecretKey: f.secret}, nil
}

func clientTo(t *testing.T, h http.HandlerFunc) (*Client, *fakeCreds) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	creds := &fakeCreds{id: "SP-1", secret: "spsk_1"}
	c := NewClient(srv.URL, creds)
	c.MaxRetries = 2
	return c, creds
}

func TestClient_GetParsesData(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"ord_1"},"meta":{"total":3,"per_page":20}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/order", nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Meta)
	assert.Equal(t, 3, resp.Meta.Total)
	assert.JSONEq(t, `{"id":"ord_1"}`, string(resp.Data))
}

func TestClient_IntoUnwrapsTopLevel(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"payment_methods":[{"payment_method":"CARD","active":1}]}`))
	})
	resp, err := c.Get(context.Background(), "/v1/merchant", nil)
	require.NoError(t, err)
	var m struct {
		PaymentMethods []struct {
			PaymentMethod string `json:"payment_method"`
		} `json:"payment_methods"`
	}
	require.NoError(t, resp.Into(&m))
	require.Len(t, m.PaymentMethods, 1)
	assert.Equal(t, "CARD", m.PaymentMethods[0].PaymentMethod)
}

func TestClient_SendsBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	c, _ := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	_, err := c.Get(context.Background(), "/v1/order", nil)
	require.NoError(t, err)
	assert.Equal(t, "SP-1", gotUser)
	assert.Equal(t, "spsk_1", gotPass)
}

func TestClient_ReMintsOnceOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"error":401,"messages":{"error":"Unauthorized"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"ord_1"}}`))
	})
	resp, err := c.Get(context.Background(), "/v1/order", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"ord_1"}`, string(resp.Data))
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClient_Persistent401MapsToAuthExit(t *testing.T) {
	c, creds := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"error":401,"messages":{"error":"Unauthorized"}}`))
	})
	_, err := c.Get(context.Background(), "/v1/order", nil)
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes), "re-mints at most once")
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 401, he.StatusCode) // cmdutil maps 401 → ExitAuth (tested there)
	assert.Contains(t, he.Message, "Unauthorized")
}

func TestClient_BusinessErrorShape(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":4001,"message":"Đơn hàng đã bị hủy trước đó"}`))
	})
	_, err := c.Post(context.Background(), "/v1/order/cancel", map[string]string{"order_invoice_number": "x"})
	require.Error(t, err)
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 400, he.StatusCode)
	assert.Equal(t, "4001", he.Code)
	assert.Contains(t, he.Message, "đã bị hủy")
}

func TestClient_RetriesOn429(t *testing.T) {
	var calls int32
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	_, err := c.Get(context.Background(), "/v1/order", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClient_RefusesRedirectAndNamesTarget(t *testing.T) {
	var hits int32
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Location", "https://elsewhere.example/v1/order")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})

	_, err := c.Get(context.Background(), "/v1/order", nil)
	require.Error(t, err)

	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, http.StatusTemporaryRedirect, he.StatusCode)
	assert.Contains(t, he.Message, "unexpected redirect")
	assert.Contains(t, he.Message, "https://elsewhere.example/v1/order", "the target must be reported")
	assert.EqualValues(t, 1, atomic.LoadInt32(&hits), "the redirect must not be followed")
}

func TestClient_MutationIsNotReplayedOn401(t *testing.T) {
	var calls int32
	c, creds := clientTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"messages":{"error":"Unauthorized"}}`))
	})
	_, err := c.Post(context.Background(), "/v1/order/voidTransaction", map[string]string{"order_invoice_number": "INV-1"})
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
			_, _ = w.Write([]byte(`{"status":401,"messages":{"error":"Unauthorized"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	})
	_, err := c.Get(context.Background(), "/v1/order/voidTransaction", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&creds.refreshes))
}

// The API server 404s checkout/init, so success proves the checkout host was used.
func TestInitCheckout_PostsToCheckoutHostNotAPIHost(t *testing.T) {
	var apiHits, checkoutHits int32
	var gotAuth, gotBody string

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&apiHits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(apiSrv.Close)
	checkoutSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&checkoutHits, 1)
		require.Equal(t, "/v1/checkout/init", r.URL.Path)
		require.NoError(t, r.ParseForm())
		gotAuth = r.Header.Get("Authorization")
		gotBody = r.PostForm.Get("signature")
		w.Header().Set("Location", "https://pay-sandbox.sepay.vn/v1/checkout?merchant=SP-1")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(checkoutSrv.Close)

	c := NewClient(apiSrv.URL, &fakeCreds{id: "SP-1", secret: "spsk_1"})
	c.CheckoutURL = checkoutSrv.URL + "/"

	loc, err := c.InitCheckout(context.Background(), CheckoutParams{
		Operation: "PURCHASE", OrderAmount: "150000", Currency: "VND", InvoiceNumber: "INV-1", Description: "d",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://pay-sandbox.sepay.vn/v1/checkout?merchant=SP-1", loc)
	assert.EqualValues(t, 1, atomic.LoadInt32(&checkoutHits))
	assert.EqualValues(t, 0, atomic.LoadInt32(&apiHits), "the REST API host must never see checkout/init")
	assert.NotEmpty(t, gotBody, "the request is authenticated by its signature")
	assert.Empty(t, gotAuth, "the secret must not be sent as a Basic header to the checkout host")
}

func TestInitCheckout_NoCheckoutHostFailsClosed(t *testing.T) {
	var hits int32
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusFound)
	})

	_, err := c.InitCheckout(context.Background(), CheckoutParams{Operation: "PURCHASE", OrderAmount: "1", Currency: "VND"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checkout host is not configured")
	assert.EqualValues(t, 0, atomic.LoadInt32(&hits), "nothing is sent when there is no checkout host")
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

func TestClient_ServerErrorCarriesRequestID(t *testing.T) {
	c, _ := clientTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "pg_req_1")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<html>oops</html>`))
	})
	_, err := c.Post(context.Background(), "/v1/order/cancel", map[string]string{"order_invoice_number": "x"})
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, 500, he.StatusCode)
	assert.Equal(t, "pg_req_1", he.RequestID)
}

func TestInitCheckout_UnreachableCheckoutHostIsATransportError(t *testing.T) {
	checkoutSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	checkoutSrv.Close()
	c := NewClient("http://127.0.0.1:1", &fakeCreds{id: "SP-1", secret: "spsk_1"})
	c.CheckoutURL = checkoutSrv.URL

	_, err := c.InitCheckout(context.Background(), CheckoutParams{Operation: "PURCHASE", OrderAmount: "1", Currency: "VND"})
	var te *api.TransportError
	require.ErrorAs(t, err, &te)
	assert.Equal(t, strings.TrimPrefix(checkoutSrv.URL, "http://")+" refused the connection; check your internet connection and retry", err.Error())
}
