package product

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func countingManager(t *testing.T) (*Manager, *int32) {
	t.Helper()
	keyring.MockInit()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"auth_type":"basic","merchant_id":"SP-1","secret_key":"spsk_x"}}`))
	}))
	t.Cleanup(srv.Close)
	m := NewManager(NewBroker(api.NewClient(srv.URL, "tok")), NewStore(t.TempDir()), "", "sandbox")
	return m, &calls
}

func TestManager_GetMintsWhenEmpty(t *testing.T) {
	m, calls := countingManager(t)
	c, err := m.Get(context.Background(), PG)
	require.NoError(t, err)
	assert.Equal(t, "SP-1", c.MerchantID)
	assert.Equal(t, int32(1), atomic.LoadInt32(calls))
}

func TestManager_GetServesFromCache(t *testing.T) {
	m, calls := countingManager(t)
	_, err := m.Get(context.Background(), PG)
	require.NoError(t, err)
	_, err = m.Get(context.Background(), PG)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(calls), "second Get should hit the cache, not re-mint")
}

func TestManager_GetReMintsExpiredBearer(t *testing.T) {
	m, calls := countingManager(t)
	expired := &Credential{Product: PG, Env: "sandbox", AuthType: authBearer, AccessToken: "old", ExpiresAt: time.Now().Add(-time.Hour)}
	_, err := m.store.Set("default", PG, "sandbox", expired, false)
	require.NoError(t, err)

	c, err := m.Get(context.Background(), PG)
	require.NoError(t, err)
	assert.Equal(t, authBasic, c.AuthType, "expired bearer should be replaced by a fresh mint")
	assert.Equal(t, int32(1), atomic.LoadInt32(calls))
}

func TestManager_RefreshAlwaysMints(t *testing.T) {
	m, calls := countingManager(t)
	_, err := m.Get(context.Background(), PG)
	require.NoError(t, err)
	_, err = m.Refresh(context.Background(), PG)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(calls))
}

func TestManager_EmptyProfileDefaults(t *testing.T) {
	m, _ := countingManager(t)
	assert.Equal(t, "default", m.profile)
}

func TestManager_GetReplacesABearerTooCloseToItsExpiry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		left  time.Duration
		mints int32
	}{
		{"comfortable life is served from cache", requestBudget + 20*time.Second, 0},
		{"less than a request budget is re-minted", requestBudget - 5*time.Second, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, calls := countingManager(t)
			cached := &Credential{
				Product: PG, Env: "sandbox", AuthType: authBearer,
				AccessToken: "cached", ExpiresAt: time.Now().Add(tc.left),
			}
			_, err := m.store.Set("default", PG, "sandbox", cached, false)
			require.NoError(t, err)

			c, err := m.Get(context.Background(), PG)
			require.NoError(t, err)
			assert.Equal(t, tc.mints, atomic.LoadInt32(calls))
			if tc.mints == 0 {
				assert.Equal(t, "cached", c.AccessToken)
			} else {
				assert.NotEqual(t, "cached", c.AccessToken)
			}
		})
	}
}
