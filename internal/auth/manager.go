package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
)

// ErrKeychainUnavailable means the OS keychain refused a write for a profile that did not choose
// file storage. Tokens never fall back to the plaintext file on their own.
var ErrKeychainUnavailable = errors.New("could not store the token in the OS keychain")

// keychainHint tells the user how to move the token to the 0600 file: a login falls back to it on its own.
const keychainHint = "to keep tokens in a 0600 file instead, run: sepay login"

// Manager stores credentials in the OS keychain, or inline in the 0600 hosts file when the
// profile keeps them there: chosen with --insecure-storage, or a login that found no usable keychain.
type Manager struct {
	hosts   *config.Hosts
	keyring credStore
}

// NewManager returns a Manager backed by the given hosts document.
func NewManager(hosts *config.Hosts) *Manager {
	return &Manager{hosts: hosts, keyring: keyringStore{}}
}

// Hosts returns the underlying hosts document.
func (m *Manager) Hosts() *config.Hosts { return m.hosts }

// Get returns the credential stored for a profile.
func (m *Manager) Get(profile string) (*Credential, error) {
	p := m.hosts.Profiles[profile]
	if p == nil {
		return nil, ErrNotFound
	}
	if p.Insecure {
		return credentialFromProfile(p), nil
	}
	return m.keyring.get(keyFor(profile, p.Host))
}

// Set stores cred under the cross-process lock; nil scopes and "" email keep the stored values.
// usedFile reports that the token went to the 0600 file, which happens only when insecure is set;
// a keychain failure otherwise returns ErrKeychainUnavailable and writes nothing.
func (m *Manager) Set(profile, host string, cred *Credential, scopes []string, insecure bool, email string) (usedFile bool, err error) {
	return m.store(profile, host, nil, cred, scopes, insecure, email)
}

// SetIfCurrent stores cred only while the profile still holds from, else returns *ProfileChangedError:
// a mismatch means the label changed hands and cred belongs to its previous holder.
func (m *Manager) SetIfCurrent(profile, host string, from, cred *Credential, scopes []string, insecure bool) (usedFile bool, err error) {
	if from == nil {
		return false, ErrNotFound
	}
	return m.store(profile, host, from, cred, scopes, insecure, "")
}

// MarkRevoked clears a profile's tokens but keeps the profile, writing only while it still holds from.
func (m *Manager) MarkRevoked(profile string, from *Credential) error {
	p := m.hosts.Profiles[profile]
	if p == nil {
		return ErrNotFound
	}
	_, err := m.SetIfCurrent(profile, p.Host, from, &Credential{}, nil, p.Insecure)
	return err
}

// store writes both stores inside the lock, so a refused conditional write never touches the keychain.
func (m *Manager) store(profile, host string, from, cred *Credential, scopes []string, insecure bool, email string) (usedFile bool, err error) {
	err = m.hosts.Update(func(h *config.Hosts) error {
		if from != nil {
			held, herr := m.heldBy(h, profile)
			if herr != nil || held == nil || held.RefreshToken != from.RefreshToken {
				return &ProfileChangedError{Profile: profile, Held: held}
			}
		}

		usedFile = insecure
		if insecure {
			// The token lives in exactly one place.
			_ = m.keyring.delete(keyFor(profile, host))
		} else if kerr := m.keyring.set(keyFor(profile, host), cred); kerr != nil {
			// Fail closed: returning aborts the update, so hosts.yml keeps its previous content.
			return fmt.Errorf("%w (%v); %s", ErrKeychainUnavailable, kerr, keychainHint) //nolint:errorlint // %v: only the sentinel is matched; the cause stays out of errors.Is/As
		}

		p := h.Profiles[profile]
		if p == nil {
			p = &config.Profile{}
			h.Profiles[profile] = p
		}
		p.Host = host
		if scopes != nil {
			p.Scopes = scopes
		}
		if email != "" {
			p.Email = email
		}
		if usedFile {
			writeInline(p, cred)
		} else {
			clearInline(p)
		}
		if h.Active == "" {
			h.Active = profile
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return usedFile, nil
}

// heldBy reads the profile under its own stored host, not the caller's.
func (m *Manager) heldBy(h *config.Hosts, profile string) (*Credential, error) {
	p := h.Profiles[profile]
	if p == nil {
		return nil, ErrNotFound
	}
	if p.Insecure {
		return credentialFromProfile(p), nil
	}
	return m.keyring.get(keyFor(profile, p.Host))
}

const refreshLockFile = ".refresh.lock"

// LockRefresh serializes spending a refresh token across processes. The server rotates the token on
// use, so two processes spending the same one leave the loser with invalid_grant and the profile
// revoked. It waits until ctx is done; ok is false when no lock was taken (another holder for the
// whole wait, Ctrl-C, or no config dir).
func (m *Manager) LockRefresh(ctx context.Context) (release func(), ok bool) {
	if m.hosts.Dir() == "" {
		return func() {}, false
	}
	lock, ok, err := config.TryLockFileContext(ctx, filepath.Join(m.hosts.Dir(), refreshLockFile))
	if err != nil || !ok {
		return func() {}, false
	}
	return lock.Release, true
}

// Current re-reads the stored credential of profile, including writes by other processes. It leaves the
// document the Manager holds alone: callers keep reading the profile they resolved, even after a logout elsewhere.
func (m *Manager) Current(profile string) (*Credential, error) {
	fresh, err := config.LoadHostsFrom(m.hosts.Dir())
	if err != nil {
		return nil, err
	}
	return m.heldBy(fresh, profile)
}

// SetActive switches the active profile; ErrNotFound if it does not exist on disk.
func (m *Manager) SetActive(profile string) error {
	return m.hosts.Update(func(h *config.Hosts) error {
		if h.Profiles[profile] == nil {
			return ErrNotFound
		}
		h.Active = profile
		return nil
	})
}

// Delete removes a profile and its credential from keychain and file.
func (m *Manager) Delete(profile string) error {
	p := m.hosts.Profiles[profile]
	if p == nil {
		return nil
	}
	dropProfile := func() error {
		return m.hosts.Update(func(h *config.Hosts) error {
			delete(h.Profiles, profile)
			// Map order is random: leave Active empty and let the caller prompt.
			if h.Active == profile {
				h.Active = ""
			}
			return nil
		})
	}
	kerr := m.keyring.delete(keyFor(profile, p.Host))
	// An insecure profile's secret is in the file, so a keychain failure must not block erasing it.
	if kerr != nil && !p.Insecure {
		return kerr
	}
	return dropProfile()
}

func keyFor(profile, host string) string { return profile + ":" + canonicalHostKey(host) }

// SameHost reports whether two stored host spellings name one host.
func SameHost(a, b string) bool { return canonicalHostKey(a) == canonicalHostKey(b) }

// canonicalHostKey keys the keychain by lowercase host plus explicit port, so spellings like
// "https://MY.SEPAY.VN." share one entry and a logout cannot strand a token under another.
func canonicalHostKey(host string) string {
	raw := strings.TrimRight(strings.TrimSpace(host), "/")
	scheme, rest := "https", raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, rest = strings.ToLower(raw[:i]), raw[i+3:]
	}
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Hostname() == "" {
		return strings.ToLower(rest)
	}
	name := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	// A schemeless host has no default port to drop, so drop both, or "localhost:80" splits from "http://localhost:80".
	schemeless := !strings.Contains(raw, "://")
	isDefaultPort := (schemeless && (port == "80" || port == "443")) ||
		(scheme == "https" && port == "443") || (scheme == "http" && port == "80")
	if port == "" || isDefaultPort {
		return name
	}
	return net.JoinHostPort(name, port)
}

func credentialFromProfile(p *config.Profile) *Credential {
	c := &Credential{
		AccessToken:  p.OAuthToken,
		RefreshToken: p.RefreshToken,
		Scope:        strings.Join(p.Scopes, " "),
	}
	if p.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil {
			c.ExpiresAt = t
		}
	}
	return c
}

func writeInline(p *config.Profile, c *Credential) {
	p.OAuthToken = c.AccessToken
	p.RefreshToken = c.RefreshToken
	p.Insecure = true
	if c.ExpiresAt.IsZero() {
		p.ExpiresAt = ""
	} else {
		p.ExpiresAt = c.ExpiresAt.Format(time.RFC3339)
	}
}

func clearInline(p *config.Profile) {
	p.OAuthToken = ""
	p.RefreshToken = ""
	p.ExpiresAt = ""
	p.Insecure = false
}
