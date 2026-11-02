package auth

import "time"

const expirySkew = 30 * time.Second

// Credential is a stored token set for one profile.
type Credential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Scope        string    `json:"scope,omitempty"`
}

// CredentialFromToken converts a Token into a Credential with an absolute ExpiresAt.
func CredentialFromToken(t *Token) *Credential {
	c := &Credential{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		Scope:        t.Scope,
	}
	if t.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return c
}

// Revoked reports whether the credential holds no token.
func (c *Credential) Revoked() bool { return c.AccessToken == "" && c.RefreshToken == "" }

// Expired reports whether the access token is past expiry minus a skew; a zero ExpiresAt never expires.
func (c *Credential) Expired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(expirySkew).After(c.ExpiresAt)
}

// String masks the tokens.
func (c *Credential) String() string {
	scope := c.Scope
	if scope == "" {
		scope = "-"
	}
	return "auth.Credential{" + scope + " *** exp=" + c.ExpiresAt.Format(time.RFC3339) + "}"
}
