// Package product brokers and caches product-native credentials (PG, eInvoice, Bank Hub).
package product

import "time"

// Product identifiers accepted by the broker.
const (
	PG       = "pg"
	EInvoice = "einvoice"
	BankHub  = "bankhub"
)

// Auth types of a broker credential.
const (
	authBasic  = "basic"
	authBearer = "bearer"
)

// requestBudget is the product clients' timeout. A bearer is re-minted while this much life remains,
// so it cannot lapse in flight.
const requestBudget = 30 * time.Second

// Credential is a broker-minted credential: PG a non-expiring Basic pair, the others expiring Bearer tokens.
type Credential struct {
	Product     string    `json:"product" yaml:"product"`
	Env         string    `json:"env" yaml:"env"`
	AuthType    string    `json:"auth_type" yaml:"auth_type"`
	MerchantID  string    `json:"merchant_id,omitempty" yaml:"merchant_id,omitempty"`
	SecretKey   string    `json:"secret_key,omitempty" yaml:"secret_key,omitempty"`
	AccessToken string    `json:"access_token,omitempty" yaml:"access_token,omitempty"`
	TokenType   string    `json:"token_type,omitempty" yaml:"token_type,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
}

// Expired reports whether a bearer has too little life left; one with no expiry counts as spent.
func (c *Credential) Expired() bool {
	if c.AuthType != authBearer {
		return false
	}
	if c.ExpiresAt.IsZero() {
		return true
	}
	return time.Now().Add(requestBudget).After(c.ExpiresAt)
}

// BasicAuth returns a PG credential's merchant_id and secret_key.
func (c *Credential) BasicAuth() (merchantID, secretKey string) {
	return c.MerchantID, c.SecretKey
}

// String masks the secret.
func (c *Credential) String() string {
	id := c.MerchantID
	if id == "" {
		id = "-"
	}
	return "product.Credential{" + c.Product + "/" + c.Env + " " + c.AuthType + " " + id + " ***}"
}
