package keychain

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func withTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := timeout
	timeout = d
	stuck.Store(false)
	t.Cleanup(func() {
		timeout = prev
		stuck.Store(false)
	})
}

func TestBounded_HangingKeychainIsUnavailable(t *testing.T) {
	withTimeout(t, 20*time.Millisecond)
	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	_, err := bounded(func() (string, error) { <-release; return "", nil })
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Less(t, time.Since(start), time.Second)

	calls := 0
	_, err = bounded(func() (string, error) { calls++; return "v", nil })
	require.ErrorIs(t, err, ErrUnavailable, "a keychain that hung once is skipped for the rest of the run")
	assert.Zero(t, calls)
}

func TestBounded_PassesAnswersThrough(t *testing.T) {
	withTimeout(t, time.Second)
	keyring.MockInit()

	require.NoError(t, Set("sepay", "k", "v"))
	v, err := Get("sepay", "k")
	require.NoError(t, err)
	assert.Equal(t, "v", v)

	require.NoError(t, Delete("sepay", "k"))
	_, err = Get("sepay", "k")
	assert.ErrorIs(t, err, ErrNotFound)

	keyring.MockInitWithError(errors.New("no secret service"))
	assert.EqualError(t, Set("sepay", "k", "v"), "no secret service")
	assert.False(t, stuck.Load(), "a keychain that answers with an error is not marked stuck")
}
