// Package auth implements the OAuth2 device-authorization-grant client (RFC 8628).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const challengeMethodS256 = "S256"

// PKCE is a code_verifier and its S256 code_challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a random S256 PKCE pair (RFC 7636).
func NewPKCE() (*PKCE, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("could not generate PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(seed)
	sum := sha256.Sum256([]byte(verifier))
	return &PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}
