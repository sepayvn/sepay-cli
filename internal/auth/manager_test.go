package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/sepayvn/sepay-cli/internal/config"
)

type failingStore struct{ setErr, getErr, delErr error }

func (s failingStore) set(string, *Credential) error { return s.setErr }
func (s failingStore) get(string) (*Credential, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return nil, ErrNotFound
}
func (s failingStore) delete(string) error { return s.delErr }

func newManager(t *testing.T) *Manager {
	t.Helper()
	keyring.MockInit()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	hosts, err := config.LoadHosts()
	require.NoError(t, err)
	return NewManager(hosts)
}

func hostsFileData(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("SEPAY_CONFIG_DIR"), "hosts.yml"))
	require.NoError(t, err)
	return string(data)
}

func TestManager_KeychainRoundtrip(t *testing.T) {
	m := newManager(t)
	usedFile, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc", RefreshToken: "ref"}, nil, false, "")
	require.NoError(t, err)
	assert.False(t, usedFile, "keychain available → no file fallback")

	got, err := m.Get("work")
	require.NoError(t, err)
	assert.Equal(t, "acc", got.AccessToken)
	assert.Equal(t, "ref", got.RefreshToken)

	assert.NotContains(t, hostsFileData(t), "acc")
	assert.Equal(t, "work", m.Hosts().Active)
}

func TestManager_SetEmailPreservedOnEmpty(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "a"}, nil, false, "dev@x.vn")
	require.NoError(t, err)
	// A refresh passes "" email and must keep the stored one.
	_, err = m.Set("work", "my.sepay.vn", &Credential{AccessToken: "b"}, nil, false, "")
	require.NoError(t, err)
	assert.Equal(t, "dev@x.vn", m.Hosts().Profiles["work"].Email)
}

func TestManager_InsecureStoresInline(t *testing.T) {
	m := newManager(t)
	usedFile, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc", RefreshToken: "ref"}, nil, true, "")
	require.NoError(t, err)
	assert.True(t, usedFile)
	assert.Contains(t, hostsFileData(t), "acc")

	got, err := m.Get("work")
	require.NoError(t, err)
	assert.Equal(t, "acc", got.AccessToken)
}

func TestManager_SwitchToInsecureClearsKeychain(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc1"}, nil, false, "")
	require.NoError(t, err)

	_, err = m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc2"}, nil, true, "")
	require.NoError(t, err)

	_, kerr := keyring.Get(keyringService, keyFor("work", "my.sepay.vn"))
	assert.ErrorIs(t, kerr, keyring.ErrNotFound, "keychain copy must be removed when switching to insecure storage")

	got, err := m.Get("work")
	require.NoError(t, err)
	assert.Equal(t, "acc2", got.AccessToken)
}

// A keychain failure must never move the token to the plaintext file on its own.
func TestManager_KeychainUnavailableRefusesInsteadOfWritingTheFile(t *testing.T) {
	m := newManager(t)
	m.keyring = failingStore{setErr: errors.New("keychain: no backend")}

	usedFile, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc", RefreshToken: "ref"}, nil, false, "")
	require.ErrorIs(t, err, ErrKeychainUnavailable)
	assert.Contains(t, err.Error(), "run: sepay login", "the error names the way out: a login falls back to the file")
	assert.False(t, usedFile)
	assert.Nil(t, m.Hosts().Profiles["work"], "a refused write leaves no profile behind")
	_, statErr := os.Stat(filepath.Join(os.Getenv("SEPAY_CONFIG_DIR"), "hosts.yml"))
	if statErr == nil {
		assert.NotContains(t, hostsFileData(t), "acc")
		assert.NotContains(t, hostsFileData(t), "ref")
	}
}

// A renewal for a keychain profile keeps the previous file content when the keychain refuses.
func TestManager_KeychainUnavailableOnRenewalKeepsTheFileUntouched(t *testing.T) {
	m := newManager(t)
	old := &Credential{AccessToken: "old-acc", RefreshToken: "old-ref"}
	_, err := m.Set("work", "my.sepay.vn", old, nil, false, "")
	require.NoError(t, err)
	before := hostsFileData(t)

	// The keychain still answers reads (it holds old) but refuses the write.
	m.keyring = renewalStore{held: old, setErr: errors.New("keychain: locked")}
	_, err = m.SetIfCurrent("work", "my.sepay.vn", old, &Credential{AccessToken: "new-acc", RefreshToken: "new-ref"}, nil, false)
	require.ErrorIs(t, err, ErrKeychainUnavailable)
	assert.Equal(t, before, hostsFileData(t))
	assert.NotContains(t, hostsFileData(t), "new-acc")
}

type renewalStore struct {
	held   *Credential
	setErr error
}

func (s renewalStore) set(string, *Credential) error   { return s.setErr }
func (s renewalStore) get(string) (*Credential, error) { return s.held, nil }
func (s renewalStore) delete(string) error             { return nil }

// Choosing --insecure-storage is the one way into the file.
func TestManager_InsecureStorageStillWritesTheFile(t *testing.T) {
	m := newManager(t)
	m.keyring = failingStore{setErr: errors.New("keychain: no backend")}

	usedFile, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc", RefreshToken: "ref"}, nil, true, "")
	require.NoError(t, err)
	assert.True(t, usedFile)
	got, err := m.Get("work")
	require.NoError(t, err)
	assert.Equal(t, "acc", got.AccessToken)
}

func TestManager_KeychainErrorIsNotNotFound(t *testing.T) {
	m := newManager(t)
	m.Hosts().Profiles["work"] = &config.Profile{Host: "my.sepay.vn"} // keychain-backed
	m.keyring = failingStore{getErr: errors.New("keychain: no backend")}

	_, err := m.Get("work")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound, "a real keychain error must be distinct from not-found")
}

func TestManager_DeleteActiveLeavesActiveEmpty(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "a"}, nil, false, "")
	require.NoError(t, err)
	_, err = m.Set("personal", "my.sepay.vn", &Credential{AccessToken: "b"}, nil, false, "")
	require.NoError(t, err)
	m.Hosts().Active = "work"

	require.NoError(t, m.Delete("work"))
	assert.Empty(t, m.Hosts().Active, "deleting the active profile leaves none active (no random auto-pick)")
	assert.Contains(t, m.Hosts().Profiles, "personal")
}

func TestManager_GetUnknownProfile(t *testing.T) {
	m := newManager(t)
	_, err := m.Get("nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestManager_Delete(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc"}, nil, false, "")
	require.NoError(t, err)

	require.NoError(t, m.Delete("work"))
	_, err = m.Get("work")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Empty(t, m.Hosts().Active)
}

func TestManager_DeleteKeychainFailureKeepsProfile(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc"}, nil, false, "")
	require.NoError(t, err)

	// A keychain delete failure must surface and keep the profile so a retry can clear the secret.
	m.keyring = failingStore{delErr: errors.New("keychain: no backend")}
	err = m.Delete("work")
	require.Error(t, err)
	assert.NotNil(t, m.Hosts().Profiles["work"], "profile kept when secret deletion fails")
}

func TestManager_DeleteMissingProfileIsNoop(t *testing.T) {
	m := newManager(t)
	assert.NoError(t, m.Delete("nope"))
}

func TestManager_DeleteInsecureProfileErasesDespiteKeychainError(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "acc"}, nil, true, "")
	require.NoError(t, err)
	assert.Contains(t, hostsFileData(t), "acc", "insecure profile stores the token inline")

	// A broken keychain must not block erasing the inline secret.
	m.keyring = failingStore{delErr: errors.New("keychain: no backend")}
	require.NoError(t, m.Delete("work"))
	assert.Nil(t, m.Hosts().Profiles["work"], "insecure profile erased")
	assert.NotContains(t, hostsFileData(t), "acc", "inline plaintext token erased")
}

func TestCanonicalHostKey(t *testing.T) {
	cases := []struct {
		name, host, want string
	}{
		{"bare host", "my.sepay.vn", "my.sepay.vn"},
		{"scheme prefix", "https://my.sepay.vn", "my.sepay.vn"},
		{"uppercase and trailing dot", "MY.SEPAY.VN.", "my.sepay.vn"},
		{"trailing slash", "https://my.sepay.vn/", "my.sepay.vn"},
		{"port is part of the identity", "http://127.0.0.1:8079", "127.0.0.1:8079"},
		{"default https port is not", "https://my.sepay.vn:443", "my.sepay.vn"},
		{"default http port is not", "http://my.sepay.vn:80", "my.sepay.vn"},
		{"schemeless default http port", "localhost:80", "localhost"},
		{"schemeless default https port", "my.sepay.vn:443", "my.sepay.vn"},
		{"schemeless real port keeps it", "203.0.113.9:8080", "203.0.113.9:8080"},
		{"unparseable value still drops the scheme", "https://my.sepay.vn:port", "my.sepay.vn:port"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, canonicalHostKey(tc.host))
		})
	}
}

func TestManager_LogoutErasesCredentialStoredUnderAHostAlias(t *testing.T) {
	m := newManager(t)
	_, err := m.Set("work", "my.sepay.vn", &Credential{AccessToken: "first"}, nil, false, "")
	require.NoError(t, err)
	_, err = m.Set("work", "https://MY.SEPAY.VN", &Credential{AccessToken: "second"}, nil, false, "")
	require.NoError(t, err)

	require.NoError(t, m.Delete("work"))
	_, err = keyring.Get(keyringService, "work:my.sepay.vn")
	assert.ErrorIs(t, err, keyring.ErrNotFound, "no credential survives logout under any spelling")
}

func TestManager_SetIfCurrentRefusesAfterTheLabelChangedHands(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "A-at", RefreshToken: "A-rt"}, nil, false, "a@corp.vn")))
	startedFrom, err := m.Get("work")
	require.NoError(t, err)

	require.NoError(t, m.Delete("work"))
	mB := NewManager(freshHosts(t))
	require.NoError(t, mustSet(mB.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "B-at", RefreshToken: "B-rt"}, nil, false, "b@corp.vn")))

	_, err = m.SetIfCurrent("work", "my.sepay.vn", startedFrom,
		&Credential{AccessToken: "A-at2", RefreshToken: "A-rt2"}, nil, false)

	var changed *ProfileChangedError
	require.ErrorAs(t, err, &changed)
	assert.Equal(t, "work", changed.Profile)
	require.NotNil(t, changed.Held, "the caller is handed what the label holds now")
	assert.Equal(t, "B-at", changed.Held.AccessToken)

	final := NewManager(freshHosts(t))
	got, gerr := final.Get("work")
	require.NoError(t, gerr)
	assert.Equal(t, "B-at", got.AccessToken, "the keychain still holds the new account's token")
	assert.Equal(t, "B-rt", got.RefreshToken)
	assert.Equal(t, "b@corp.vn", final.Hosts().Profiles["work"].Email)
}

func TestManager_SetIfCurrentWritesWhenTheLabelIsUnchanged(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		t.Run(map[bool]string{false: "keychain", true: "inline"}[insecure], func(t *testing.T) {
			m := newManager(t)
			require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
				&Credential{AccessToken: "at1", RefreshToken: "rt1"}, []string{"sandbox"}, insecure, "a@corp.vn")))
			startedFrom, err := m.Get("work")
			require.NoError(t, err)

			_, err = m.SetIfCurrent("work", "my.sepay.vn",
				startedFrom, &Credential{AccessToken: "at2", RefreshToken: "rt2"}, []string{"sandbox"}, insecure)
			require.NoError(t, err)

			final := NewManager(freshHosts(t))
			got, gerr := final.Get("work")
			require.NoError(t, gerr)
			assert.Equal(t, "at2", got.AccessToken)
			assert.Equal(t, "rt2", got.RefreshToken)
			assert.Equal(t, "a@corp.vn", final.Hosts().Profiles["work"].Email, "email is preserved, as before")
		})
	}
}

func TestManager_SetIfCurrentHandsBackThePeersToken(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "at1", RefreshToken: "rt1"}, nil, false, "")))
	startedFrom, err := m.Get("work")
	require.NoError(t, err)

	peer := NewManager(freshHosts(t))
	require.NoError(t, mustSet(peer.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "peer-at", RefreshToken: "peer-rt"}, nil, false, "")))

	_, err = m.SetIfCurrent("work", "my.sepay.vn", startedFrom,
		&Credential{AccessToken: "loser-at", RefreshToken: "loser-rt"}, nil, false)
	var changed *ProfileChangedError
	require.ErrorAs(t, err, &changed)
	assert.Equal(t, "peer-at", changed.Held.AccessToken)
}

func TestManager_SetIfCurrentRefusesAfterLogout(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "at1", RefreshToken: "rt1"}, nil, false, "")))
	startedFrom, err := m.Get("work")
	require.NoError(t, err)
	require.NoError(t, m.Delete("work"))

	_, err = m.SetIfCurrent("work", "my.sepay.vn", startedFrom,
		&Credential{AccessToken: "at2", RefreshToken: "rt2"}, nil, false)
	var changed *ProfileChangedError
	require.ErrorAs(t, err, &changed)
	assert.Nil(t, changed.Held, "nothing to fall back to")

	final := NewManager(freshHosts(t))
	_, gerr := final.Get("work")
	assert.ErrorIs(t, gerr, ErrNotFound, "the profile stays gone")
}

func TestManager_SetIfCurrentRefusesWhenTheHeldCredentialIsUnreadable(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "at1", RefreshToken: "rt1"}, nil, false, "")))
	startedFrom, err := m.Get("work")
	require.NoError(t, err)

	m.keyring = failingStore{getErr: errors.New("keychain: no backend")}
	_, err = m.SetIfCurrent("work", "my.sepay.vn", startedFrom,
		&Credential{AccessToken: "at2", RefreshToken: "rt2"}, nil, false)
	var changed *ProfileChangedError
	assert.ErrorAs(t, err, &changed)
}

func TestManager_SetStaysUnconditional(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "A-at", RefreshToken: "A-rt"}, nil, false, "a@corp.vn")))
	require.NoError(t, mustSet(m.Set("work", "my.sepay.vn",
		&Credential{AccessToken: "B-at", RefreshToken: "B-rt"}, nil, false, "b@corp.vn")))

	got, err := m.Get("work")
	require.NoError(t, err)
	assert.Equal(t, "B-at", got.AccessToken)
	assert.Equal(t, "b@corp.vn", m.Hosts().Profiles["work"].Email)
}

func TestManager_SetIfCurrentNeedsSomethingToCompare(t *testing.T) {
	m := newManager(t)
	_, err := m.SetIfCurrent("work", "my.sepay.vn", nil, &Credential{AccessToken: "at"}, nil, false)
	assert.ErrorIs(t, err, ErrNotFound)
}

func mustSet(_ bool, err error) error { return err }

func freshHosts(t *testing.T) *config.Hosts {
	t.Helper()
	h, err := config.LoadHosts()
	require.NoError(t, err)
	return h
}

// Without server identity every login lands on "default"; the rotating refresh token tells accounts apart.
func TestManager_SetIfCurrentGuardsTheDefaultLabelWithoutAnyEmail(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("default", "my.sepay.vn",
		&Credential{AccessToken: "A-at", RefreshToken: "A-rt"}, nil, false, "")))
	startedFrom, err := m.Get("default")
	require.NoError(t, err)
	require.Empty(t, m.Hosts().Profiles["default"].Email, "the premise: no identity to compare")

	require.NoError(t, m.Delete("default"))
	mB := NewManager(freshHosts(t))
	require.NoError(t, mustSet(mB.Set("default", "my.sepay.vn",
		&Credential{AccessToken: "B-at", RefreshToken: "B-rt"}, nil, false, "")))
	require.Empty(t, mB.Hosts().Profiles["default"].Email)

	_, err = m.SetIfCurrent("default", "my.sepay.vn", startedFrom,
		&Credential{AccessToken: "A-at2", RefreshToken: "A-rt2"}, nil, false)

	var changed *ProfileChangedError
	require.ErrorAs(t, err, &changed, "refused with no email on either side")
	assert.Equal(t, "B-at", changed.Held.AccessToken)

	final := NewManager(freshHosts(t))
	got, gerr := final.Get("default")
	require.NoError(t, gerr)
	assert.Equal(t, "B-at", got.AccessToken)
}

func TestManager_SetIfCurrentLeavesTheKeychainUntouchedOnRefusal(t *testing.T) {
	m := newManager(t)
	require.NoError(t, mustSet(m.Set("shared", "my.sepay.vn",
		&Credential{AccessToken: "A-at", RefreshToken: "A-rt"}, nil, false, "a@corp.vn")))
	startedFrom, err := m.Get("shared")
	require.NoError(t, err)

	mB := NewManager(freshHosts(t))
	require.NoError(t, mustSet(mB.Set("shared", "my.sepay.vn",
		&Credential{AccessToken: "B-at", RefreshToken: "B-rt"}, nil, false, "b@corp.vn")))

	_, err = m.SetIfCurrent("shared", "my.sepay.vn", startedFrom,
		&Credential{AccessToken: "A-at2", RefreshToken: "A-rt2"}, nil, false)
	require.Error(t, err)

	raw, kerr := keyring.Get(keyringService, keyFor("shared", "my.sepay.vn"))
	require.NoError(t, kerr)
	assert.Contains(t, raw, "B-at", "the keychain still holds the current account's token")
	assert.NotContains(t, raw, "A-at2")
}
