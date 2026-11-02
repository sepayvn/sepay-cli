package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDebugTransport_logsWithoutCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const token = "super-secret-token-value"
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	client := &http.Client{Transport: DebugTransport(nil)}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/transactions", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	out := logs.String()
	assert.Contains(t, out, "msg=request")
	assert.Contains(t, out, "msg=response")
	assert.Contains(t, out, "status=200")
	assert.Contains(t, out, "Authorization") // the header name is fine…
	assert.NotContains(t, out, token)        // …its value never is
}

func TestDebugTransport_silentBelowDebugLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	client := &http.Client{Transport: DebugTransport(nil)}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Empty(t, strings.TrimSpace(logs.String()))
}

func TestDebugURL_masksIdentifyingQueryValues(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"search term", "https://h/v2/transactions?per_page=1&q=0123456789", "q=****6789"},
		{"account number", "https://h/v2/transactions?account_number=0000000002", "account_number=****0002"},
		{"short value fully masked", "https://h/v2/transactions?q=12", "q=****"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			require.NoError(t, err)
			got := debugURL(u)
			assert.Contains(t, got, tc.want)
			assert.NotContains(t, got, "0123456789")
			assert.NotContains(t, got, "0000000002")
		})
	}
}

func TestDebugURL_leavesOrdinaryQueryAlone(t *testing.T) {
	u, err := url.Parse("https://h/v2/transactions?per_page=1&transfer_type=in")
	require.NoError(t, err)
	assert.Equal(t, u.String(), debugURL(u))
}

// Masking defaults to on, so a filter param nobody enumerated is still covered.
func TestDebugURL_masksAnyParamNotExplicitlySafe(t *testing.T) {
	for _, tc := range []struct{ name, raw, secret string }{
		{"customer id from --customer", "https://h/v1/order?customer_id=CUS-0000000001", "CUS-0000000001"},
		{"company xid", "https://h/v1/transaction?company_xid=cmp_abc123", "cmp_abc123"},
		{"bank account xid", "https://h/v1/transaction?bank_account_xid=ba_secret99", "ba_secret99"},
		{"tax code via sepay api -f", "https://h/v1/invoice?tax_code=9999999999", "9999999999"},
		{"arbitrary param via sepay api -f", "https://h/v2/x?access_token=eyJraWQiOiJz", "eyJraWQiOiJz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			require.NoError(t, err)
			got := debugURL(u)
			assert.NotContains(t, got, tc.secret)
			assert.Contains(t, got, "****")
		})
	}
}

func TestDebugURL_keepsPagingAndEnumsReadable(t *testing.T) {
	u, err := url.Parse("https://h/v1/order?page=2&per_page=50&order_status=CAPTURED&from_created_at=2026-08-01")
	require.NoError(t, err)
	got := debugURL(u)
	for _, want := range []string{"page=2", "per_page=50", "order_status=CAPTURED", "from_created_at=2026-08-01"} {
		assert.Contains(t, got, want)
	}
}
