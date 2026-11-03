// Package keychain bounds every OS keychain call, so a secret service that never answers counts as
// unavailable instead of hanging. sepay login then keeps the token in the 0600 file; every other
// write fails closed.
package keychain

import (
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/zalando/go-keyring"
)

// ErrNotFound means the keychain answered and holds no entry for the key.
var ErrNotFound = keyring.ErrNotFound

// ErrUnavailable means the keychain did not answer in time.
var ErrUnavailable = errors.New("OS keychain did not answer")

// A headless Linux box cannot show an unlock prompt, so a locked Secret Service waits forever there;
// elsewhere the wait may be a person answering an access prompt.
const (
	headlessTimeout    = 3 * time.Second
	interactiveTimeout = 2 * time.Minute
)

var (
	timeout = defaultTimeout()
	// stuck skips the keychain for the rest of the run once it failed to answer, so logout and
	// eviction don't each wait out the timeout again.
	stuck atomic.Bool
)

func defaultTimeout() time.Duration {
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return headlessTimeout
	}
	return interactiveTimeout
}

// Get returns the secret stored under service/key.
func Get(service, key string) (string, error) {
	return bounded(func() (string, error) { return keyring.Get(service, key) })
}

// Set stores value under service/key.
func Set(service, key, value string) error {
	_, err := bounded(func() (string, error) { return "", keyring.Set(service, key, value) })
	return err
}

// Delete removes service/key; a missing entry is ErrNotFound.
func Delete(service, key string) error {
	_, err := bounded(func() (string, error) { return "", keyring.Delete(service, key) })
	return err
}

func bounded(call func() (string, error)) (string, error) {
	if stuck.Load() {
		return "", ErrUnavailable
	}
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		v, err := call()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.value, r.err
	case <-time.After(timeout):
		stuck.Store(true)
		return "", ErrUnavailable
	}
}
