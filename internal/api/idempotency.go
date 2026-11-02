package api

import (
	"crypto/rand"
	"encoding/hex"
)

// NewIdempotencyKey returns a random key for a mutating request. Generate it
// once per logical operation and reuse it across retries via WriteOptions so a
// replay does not create a duplicate.
func NewIdempotencyKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
