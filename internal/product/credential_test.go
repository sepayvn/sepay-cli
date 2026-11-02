package product

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCredential_ExpiredKeepsAWholeRequestBudgetInHand(t *testing.T) {
	bearer := func(d time.Duration) *Credential {
		return &Credential{AuthType: authBearer, AccessToken: "tok", ExpiresAt: time.Now().Add(d)}
	}
	assert.False(t, bearer(requestBudget+10*time.Second).Expired(), "enough life for a full request")
	assert.True(t, bearer(requestBudget-time.Second).Expired(), "a request could outlive it")
	assert.True(t, bearer(-time.Minute).Expired())

	assert.False(t, bearer(59*time.Second).Expired())
	assert.True(t, bearer(29*time.Second).Expired())
}

func TestCredential_BearerWithNoExpiryIsSpent(t *testing.T) {
	assert.True(t, (&Credential{AuthType: authBearer, AccessToken: "tok"}).Expired())
}

func TestCredential_BasicNeverExpires(t *testing.T) {
	assert.False(t, (&Credential{AuthType: authBasic, MerchantID: "SP-1", SecretKey: "spsk_1"}).Expired())
}
