package auth

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
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
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, key, string(data)); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

func (keyringStore) get(key string) (*Credential, error) {
	data, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
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
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}
