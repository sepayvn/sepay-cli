package product

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
)

// Broker mints product credentials via POST /v2/products/{product}/token.
type Broker struct {
	client *api.Client
}

// NewBroker returns a Broker that mints through the given v2 API client.
func NewBroker(client *api.Client) *Broker { return &Broker{client: client} }

type brokerData struct {
	AuthType    string `json:"auth_type"`
	MerchantID  string `json:"merchant_id"`
	SecretKey   string `json:"secret_key"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresAt   string `json:"expires_at"`
}

// Mint requests a fresh credential for prod on env; the broker is sandbox-only.
func (b *Broker) Mint(ctx context.Context, prod, env string) (*Credential, error) {
	env2, err := b.client.Post(ctx, "/products/"+prod+"/token", api.WriteOptions{})
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%s credentials are sandbox-only (production not supported yet)", prod)
		}
		return nil, err
	}

	data, err := api.DataInto[brokerData](env2)
	if err != nil {
		return nil, err
	}
	return data.credential(prod, env)
}

func (d brokerData) credential(prod, env string) (*Credential, error) {
	c := &Credential{Product: prod, Env: env, AuthType: d.AuthType}
	switch d.AuthType {
	case authBasic:
		if d.MerchantID == "" || d.SecretKey == "" {
			return nil, fmt.Errorf("broker returned an incomplete basic credential for %s", prod)
		}
		c.MerchantID, c.SecretKey = d.MerchantID, d.SecretKey
	case authBearer:
		if d.AccessToken == "" {
			return nil, fmt.Errorf("broker returned an incomplete bearer credential for %s", prod)
		}
		c.AccessToken, c.TokenType = d.AccessToken, d.TokenType
		if d.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, d.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("broker returned an invalid expires_at for %s: %w", prod, err)
			}
			c.ExpiresAt = t
		}
	default:
		return nil, fmt.Errorf("broker returned an unknown auth_type %q for %s", d.AuthType, prod)
	}
	return c, nil
}
