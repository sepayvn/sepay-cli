package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RFC 7636 Appendix B worked example: this exact verifier must yield this
// exact challenge. It pins the S256 derivation independently of NewPKCE.
func TestPKCE_RFC7636Vector(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	sum := sha256.Sum256([]byte(verifier))
	assert.Equal(t, challenge, base64.RawURLEncoding.EncodeToString(sum[:]))
}

func TestNewPKCE(t *testing.T) {
	p, err := NewPKCE()
	require.NoError(t, err)

	assert.Len(t, p.Verifier, 43, "32 random bytes → 43 base64url chars")

	sum := sha256.Sum256([]byte(p.Verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), p.Challenge,
		"challenge must be S256(verifier)")

	other, err := NewPKCE()
	require.NoError(t, err)
	assert.NotEqual(t, p.Verifier, other.Verifier, "each call must be random")
}
