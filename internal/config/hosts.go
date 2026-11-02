package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// HostsFile is the credential document's name inside the config directory.
const HostsFile = "hosts.yml"

// Profile is one stored account. Token fields are set only when stored in this file instead of the keychain.
type Profile struct {
	Host   string   `yaml:"host"`
	Scopes []string `yaml:"scopes,omitempty"`
	Email  string   `yaml:"email,omitempty"`

	OAuthToken   string `yaml:"oauth_token,omitempty"`
	RefreshToken string `yaml:"refresh_token,omitempty"`
	ExpiresAt    string `yaml:"expires_at,omitempty"`
	Insecure     bool   `yaml:"insecure_storage,omitempty"`
}

// Hosts is the hosts.yml document: named profiles plus the active one.
type Hosts struct {
	Active   string              `yaml:"active,omitempty"`
	Profiles map[string]*Profile `yaml:"profiles,omitempty"`

	dir string
}

// LoadHosts reads hosts.yml from the standard config directory; a missing file is empty.
func LoadHosts() (*Hosts, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return LoadHostsFrom(dir)
}

// LoadHostsFrom reads hosts.yml from dir; a missing file is empty.
func LoadHostsFrom(dir string) (*Hosts, error) {
	h := &Hosts{Profiles: map[string]*Profile{}, dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, HostsFile))
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, h); err != nil {
		return nil, fmt.Errorf("could not read %s: %w", HostsFile, err)
	}
	if h.Profiles == nil {
		h.Profiles = map[string]*Profile{}
	}
	return h, nil
}

// Dir returns the config directory this document was loaded from.
func (h *Hosts) Dir() string { return h.dir }

// Save writes hosts.yml atomically as 0600 without locking; mutations go through Update.
func (h *Hosts) Save() error {
	data, err := yaml.Marshal(h)
	if err != nil {
		return err
	}
	return WriteFileAtomic(h.dir, HostsFile, data)
}

// Update applies mutate to the on-disk document under the cross-process lock and writes it atomically.
// An error from mutate aborts the write.
func (h *Hosts) Update(mutate func(*Hosts) error) error {
	release, err := LockDir(h.dir)
	if err != nil {
		return err
	}
	defer release()

	fresh, err := LoadHostsFrom(h.dir)
	if err != nil {
		return err
	}
	if err := mutate(fresh); err != nil {
		return err
	}
	if err := fresh.Save(); err != nil {
		return err
	}
	*h = *fresh // reflect the merged state in the receiver the Manager holds
	return nil
}

// LoosePerms reports whether dir/name is accessible beyond its owner; always false on Windows.
func LoosePerms(dir, name string) (bool, error) {
	if runtime.GOOS == "windows" {
		return false, nil
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o077 != 0, nil
}
