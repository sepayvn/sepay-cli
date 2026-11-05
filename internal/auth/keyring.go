package auth

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sepayvn/sepay-cli/internal/keychain"
)

const keyringService = "sepay"

type credStore interface {
	set(key string, c *Credential) error
	get(key string) (*Credential, error)
	delete(key string) error
}

// keyringStore stores one JSON blob per key; a missing entry is ErrNotFound, anything else a keychain error.
type keyringStore struct{}

func (keyringStore) set(key string, c *Credential) error {
	data, err := json.Marshal(c) //nolint:gosec // G117: serialized into the OS keychain, its intended store
	if err != nil {
		return err
	}
	if err := keychain.Set(keyringService, key, string(data)); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

func (keyringStore) get(key string) (*Credential, error) {
	data, err := keychain.Get(keyringService, key)
	if errors.Is(err, keychain.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("keychain: %w", err)
	}
	var c Credential
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (keyringStore) delete(key string) error {
	err := keychain.Delete(keyringService, key)
	if errors.Is(err, keychain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}
