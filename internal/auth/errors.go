package auth

import (
	"errors"
	"fmt"
)

// ErrNotFound means no credential is stored for the requested profile.
var ErrNotFound = errors.New("no stored credential")

// ProfileChangedError is a refused conditional write: the profile no longer holds the credential read
// before the exchange. Held is what it holds now, or nil.
type ProfileChangedError struct {
	Profile string
	Held    *Credential
}

func (e *ProfileChangedError) Error() string {
	return fmt.Sprintf("profile %s changed while its token was being renewed", e.Profile)
}

// OAuth device-flow error codes (RFC 8628 §3.5).
const (
	CodeAuthorizationPending = "authorization_pending"
	CodeSlowDown             = "slow_down"
	CodeExpiredToken         = "expired_token"
	CodeAccessDenied         = "access_denied"
)

// Error is an OAuth host error response carrying the RFC error code.
type Error struct {
	Code        string
	Description string
	StatusCode  int
}

func (e *Error) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("OAuth error %q: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("OAuth error %q", e.Code)
}
