package product

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func basicCred() *Credential {
	return &Credential{Product: PG, Env: "sandbox", AuthType: authBasic, MerchantID: "SP-1", SecretKey: "spsk_x"}
}

func TestStore_KeychainRoundtrip(t *testing.T) {
	keyring.MockInit()
	s := NewStore(t.TempDir())

	usedFile, err := s.Set("default", PG, "sandbox", basicCred(), false)
	require.NoError(t, err)
	assert.False(t, usedFile)

	got, err := s.Get("default", PG, "sandbox")
	require.NoError(t, err)
	assert.Equal(t, "SP-1", got.MerchantID)
	assert.Equal(t, "spsk_x", got.SecretKey)

	_, statErr := os.Stat(s.path())
	assert.True(t, os.IsNotExist(statErr))
}

func TestStore_GetMissing(t *testing.T) {
	keyring.MockInit()
	s := NewStore(t.TempDir())
	_, err := s.Get("default", PG, "sandbox")
	assert.ErrorIs(t, err, ErrNotCached)
}

func TestStore_InsecureWritesFile0600(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s := NewStore(dir)

	usedFile, err := s.Set("default", PG, "sandbox", basicCred(), true)
	require.NoError(t, err)
	assert.True(t, usedFile)

	info, err := os.Stat(s.path())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := s.Get("default", PG, "sandbox")
	require.NoError(t, err)
	assert.Equal(t, "spsk_x", got.SecretKey)
}

// A keychain failure must never move the credential to products.yml on its own.
func TestStore_KeychainUnavailableRefusesInsteadOfWritingTheFile(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	dir := t.TempDir()
	s := NewStore(dir)

	usedFile, err := s.Set("default", PG, "sandbox", basicCred(), false)
	require.ErrorIs(t, err, ErrKeychainUnavailable)
	assert.Contains(t, err.Error(), "--insecure-storage")
	assert.False(t, usedFile)
	_, statErr := os.Stat(dir + "/" + ProductsFile)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing written to the plaintext file")

	_, err = s.Get("default", PG, "sandbox")
	assert.ErrorIs(t, err, ErrNotCached)
}

// Choosing file storage still works without a keychain.
func TestStore_InsecureWritesTheFileWithoutAKeychain(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	s := NewStore(t.TempDir())

	usedFile, err := s.Set("default", PG, "sandbox", basicCred(), true)
	require.NoError(t, err)
	assert.True(t, usedFile)
	got, err := s.Get("default", PG, "sandbox")
	require.NoError(t, err)
	assert.Equal(t, "spsk_x", got.SecretKey)
}

func TestStore_EvictProfileRemovesAllProductsBothStores(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s := NewStore(dir)

	_, err := s.Set("acme", PG, "sandbox", basicCred(), false)
	require.NoError(t, err)
	_, err = s.Set("acme", EInvoice, "production", basicCred(), true) // file fallback
	require.NoError(t, err)
	_, err = s.Set("other", PG, "sandbox", basicCred(), false)
	require.NoError(t, err)

	require.NoError(t, s.EvictProfile("acme"))

	_, err = s.Get("acme", PG, "sandbox")
	assert.ErrorIs(t, err, ErrNotCached, "keychain cred evicted")
	_, err = s.Get("acme", EInvoice, "production")
	assert.ErrorIs(t, err, ErrNotCached, "file-fallback cred evicted")

	got, err := s.Get("other", PG, "sandbox")
	require.NoError(t, err, "other profile untouched")
	assert.Equal(t, "SP-1", got.MerchantID)
}

func TestStore_EvictProfileNoOpWhenEmpty(t *testing.T) {
	keyring.MockInit()
	s := NewStore(t.TempDir())
	assert.NoError(t, s.EvictProfile("nobody"))
}

// fakeKeychain fails Get/Set/Delete independently, which go-keyring's mock cannot.
type fakeKeychain struct {
	entries   map[string]string
	getErr    error
	deleteErr error
}

func (f *fakeKeychain) Get(_, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	if v, ok := f.entries[key]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}

func (f *fakeKeychain) Set(_, key, value string) error {
	f.entries[key] = value
	return nil
}

func (f *fakeKeychain) Delete(_, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.entries[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.entries, key)
	return nil
}

func TestStore_InsecureSetRefusesToLeaveAShadowingKeychainCopy(t *testing.T) {
	kr := &fakeKeychain{entries: map[string]string{}}
	s := &Store{dir: t.TempDir(), kr: kr}
	_, err := s.Set("acme", PG, "production", basicCred(), false)
	require.NoError(t, err)

	kr.deleteErr = errors.New("keychain entry is locked")
	_, err = s.Set("acme", PG, "production",
		&Credential{Product: PG, Env: "production", AuthType: authBasic, MerchantID: "SP-2", SecretKey: "spsk_new"}, true)
	require.Error(t, err, "a shadowing keychain copy must not be reported as a clean write")
	assert.Contains(t, err.Error(), "shadow")

	kr.getErr = errors.New("no secret service")
	usedFile, err := s.Set("acme", PG, "production",
		&Credential{Product: PG, Env: "production", AuthType: authBasic, MerchantID: "SP-3", SecretKey: "spsk_file"}, true)
	require.NoError(t, err)
	assert.True(t, usedFile)
}

func TestStore_EvictProfileReportsAKeychainFailure(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s := NewStore(dir)
	_, err := s.Set("acme", PG, "production", basicCred(), false)
	require.NoError(t, err)
	_, err = s.Set("acme", EInvoice, "sandbox", basicCred(), true)
	require.NoError(t, err)

	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	evictErr := s.EvictProfile("acme")
	require.Error(t, evictErr, "a failed keychain delete must reach the caller")

	keyring.MockInit()
	_, err = s.Get("acme", EInvoice, "sandbox")
	assert.ErrorIs(t, err, ErrNotCached)
}
