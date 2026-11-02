package auth

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialFromToken_ResolvesExpiry(t *testing.T) {
	before := time.Now()
	c := CredentialFromToken(&Token{AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600, Scope: "sandbox"})
	assert.Equal(t, "a", c.AccessToken)
	assert.Equal(t, "r", c.RefreshToken)
	assert.Equal(t, "sandbox", c.Scope)
	assert.WithinDuration(t, before.Add(time.Hour), c.ExpiresAt, 5*time.Second)
}

func TestCredential_Expired(t *testing.T) {
	assert.False(t, (&Credential{}).Expired(), "zero expiry never expires")
	assert.True(t, (&Credential{ExpiresAt: time.Now().Add(-time.Minute)}).Expired())
	assert.True(t, (&Credential{ExpiresAt: time.Now().Add(10 * time.Second)}).Expired(), "within skew")
	assert.False(t, (&Credential{ExpiresAt: time.Now().Add(time.Hour)}).Expired())
}

// ProfileChangedError carries a Credential, which puts a refresh token one format
// verb away from a terminal or a log line.
func TestCredential_StringMasksTheTokens(t *testing.T) {
	c := &Credential{AccessToken: "at_supersecret", RefreshToken: "rt_supersecret", Scope: "read webhook:write"}
	for _, rendered := range []string{
		c.String(),
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%v", &ProfileChangedError{Profile: "work", Held: c}),
		fmt.Errorf("wrapped: %w", &ProfileChangedError{Profile: "work", Held: c}).Error(),
	} {
		assert.NotContains(t, rendered, "at_supersecret", "access token must never render")
		assert.NotContains(t, rendered, "rt_supersecret", "refresh token must never render")
	}
	assert.Contains(t, c.String(), "***")

	// Persistence is unaffected: the keychain round-trip marshals fields, not String().
	data, err := json.Marshal(c)
	require.NoError(t, err)
	var back Credential
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, "at_supersecret", back.AccessToken, "the stored value must survive")
	assert.Equal(t, "rt_supersecret", back.RefreshToken)
}
