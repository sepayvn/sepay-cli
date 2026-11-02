package pg

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
)

// CheckoutParams are the checkout/init fields; merchant comes from the credential.
type CheckoutParams struct {
	Operation     string // PURCHASE | VERIFY
	PaymentMethod string // BANK_TRANSFER | CARD | NAPAS_BANK_TRANSFER (optional)
	OrderAmount   string
	Currency      string // VND
	InvoiceNumber string
	Description   string
	CustomerID    string
	SuccessURL    string
	ErrorURL      string
	CancelURL     string
}

// InitCheckout POSTs a signed checkout/init and returns the payment page URL (the 302 Location).
// Only the signature is sent, never the secret. Field order must match PaymentGatewayFeature::signFields.
func (c *Client) InitCheckout(ctx context.Context, p CheckoutParams) (string, error) {
	checkoutURL := strings.TrimRight(c.CheckoutURL, "/")
	if checkoutURL == "" {
		return "", fmt.Errorf("checkout host is not configured")
	}
	merchantID, secret, err := c.basic(ctx, false)
	if err != nil {
		return "", err
	}

	// Server signs only non-empty fields, in its whitelist order.
	ordered := [][2]string{
		{"merchant", merchantID},
		{"operation", p.Operation},
		{"payment_method", p.PaymentMethod},
		{"order_amount", p.OrderAmount},
		{"currency", p.Currency},
		{"order_invoice_number", p.InvoiceNumber},
		{"order_description", p.Description},
		{"customer_id", p.CustomerID},
		{"success_url", p.SuccessURL},
		{"error_url", p.ErrorURL},
		{"cancel_url", p.CancelURL},
	}
	var signParts, bodyParts []string
	for _, kv := range ordered {
		if kv[1] == "" {
			continue
		}
		signParts = append(signParts, kv[0]+"="+kv[1])
		bodyParts = append(bodyParts, kv[0]+"="+url.QueryEscape(kv[1]))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.Join(signParts, ",")))
	bodyParts = append(bodyParts, "signature="+url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil))))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, checkoutURL+"/v1/checkout/init", strings.NewReader(strings.Join(bodyParts, "&")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", api.UserAgent())

	noRedirect := &http.Client{
		Timeout:       c.httpClient.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     api.DebugTransport(nil),
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusFound, http.StatusMovedPermanently, http.StatusSeeOther, http.StatusTemporaryRedirect:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("checkout/init returned %d with no Location header", resp.StatusCode)
		}
		return loc, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		slog.Debug("checkout/init error body", "status", resp.StatusCode, "body", string(body))
		return "", fmt.Errorf("checkout/init failed (%d): %s", resp.StatusCode, checkoutErrorMessage(resp.Header.Get("Content-Type"), body))
	}
}

// checkoutErrorMessage summarizes a body that is an HTML page carrying no reason.
func checkoutErrorMessage(contentType string, body []byte) string {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return "no response body"
	}
	if strings.Contains(contentType, "html") || strings.HasPrefix(msg, "<") {
		return "rejected with an error page that carries no reason; verify the flags, then check the checkout log server-side"
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
