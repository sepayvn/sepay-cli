// Package webhook implements "sepay webhook" and its verbs (listen, test,
// list, get, create, update, delete, logs, replay).
package webhook

import (
	"strconv"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// Webhook is a webhook from the API; any response may carry secret fields, so every render masks them.
type Webhook struct {
	ID              string                     `json:"id" yaml:"id"`
	Name            string                     `json:"name" yaml:"name"`
	EventType       string                     `json:"event_type" yaml:"event_type"`
	AuthenType      string                     `json:"authen_type" yaml:"authen_type"`
	WebhookURL      string                     `json:"webhook_url" yaml:"webhook_url"`
	DeliveryMode    string                     `json:"delivery_mode,omitempty" yaml:"delivery_mode,omitempty"`
	IsVerifyPayment bool                       `json:"is_verify_payment" yaml:"is_verify_payment"`
	SkipIfNoCode    bool                       `json:"skip_if_no_code" yaml:"skip_if_no_code"`
	OnlyVA          bool                       `json:"only_va" yaml:"only_va"`
	BankMode        string                     `json:"bank_mode" yaml:"bank_mode"`
	BankAccountXID  *string                    `json:"bank_account_xid" yaml:"bank_account_xid"`
	BankAccountXIDs []string                   `json:"bank_account_xids,omitempty" yaml:"bank_account_xids,omitempty"`
	BankConfig      map[string]BankConfigEntry `json:"bank_config,omitempty" yaml:"bank_config,omitempty"`
	SubAccountXIDs  []string                   `json:"bank_sub_account_xids" yaml:"bank_sub_account_xids"`
	PrefixFilters   []string                   `json:"prefix_filters" yaml:"prefix_filters"`
	AlertThreshold  *int                       `json:"alert_threshold" yaml:"alert_threshold"`
	// Server enum ("failure", "recovery"); null when alerting was never set up. No omitempty.
	AlertEventTypes []string `json:"alert_event_types" yaml:"alert_event_types"`
	// No omitempty: null means "never configured for non-2xx retries".
	RetryConditions *RetryConditions `json:"retry_conditions" yaml:"retry_conditions"`
	Active          api.FlexBool     `json:"active" yaml:"active"`
	CreatedAt       string           `json:"created_at" yaml:"created_at"`

	// Provisioned by an onboarding partner; deliveries go to the partner's stream. No omitempty.
	ManagedByPartner bool `json:"managed_by_partner" yaml:"managed_by_partner"`

	RequestContentType   string `json:"request_content_type,omitempty" yaml:"request_content_type,omitempty"`
	APIKey               string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	SecretKey            string `json:"secret_key,omitempty" yaml:"secret_key,omitempty"`
	OAuth2ClientID       string `json:"oauth2_client_id,omitempty" yaml:"oauth2_client_id,omitempty"`
	OAuth2ClientSecret   string `json:"oauth2_client_secret,omitempty" yaml:"oauth2_client_secret,omitempty"`
	OAuth2AccessTokenURL string `json:"oauth2_access_token_url,omitempty" yaml:"oauth2_access_token_url,omitempty"`
}

// RetryConditions names the delivery outcomes SePay re-sends beyond a transport failure.
type RetryConditions struct {
	Non2xxStatusCode int `json:"non_2xx_status_code" yaml:"non_2xx_status_code"`
}

func (w Webhook) retriesNon2xx() bool {
	return w.RetryConditions != nil && w.RetryConditions.Non2xxStatusCode != 0
}

// deliveryModeListen marks a sandbox webhook with no HTTP destination; empty means HTTP.
const deliveryModeListen = "listen"

const deliveryModeHTTP = "http"

// logStatusListen is the webhook-log status of a stream delivery (a different vocabulary from delivery_mode).
const logStatusListen = "listen"

func (w Webhook) listenOnly() bool { return w.DeliveryMode == deliveryModeListen }

func (w Webhook) urlLabel() string {
	if w.listenOnly() {
		return "(listen-only)"
	}
	return w.WebhookURL
}

const redactedMark = "***"

// redacted masks the auth secrets so --json / -o yaml never carry them.
func (w Webhook) redacted() Webhook {
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		return redactedMark
	}
	w.APIKey = mask(w.APIKey)
	w.SecretKey = mask(w.SecretKey)
	w.OAuth2ClientSecret = mask(w.OAuth2ClientSecret)
	return w
}

// BankConfigEntry is one account's targeting config in a multi-account webhook.
type BankConfigEntry struct {
	ReceivePrimary bool   `json:"receive_primary" yaml:"receive_primary"`
	VaMode         string `json:"va_mode" yaml:"va_mode"`
}

// Log is one delivery-log entry from GET /v2/webhook-logs, addressed by transaction + webhook.
type Log struct {
	TransactionID      *string `json:"transaction_id" yaml:"transaction_id"`
	WebhookXID         *string `json:"webhook_xid" yaml:"webhook_xid"`
	Replayable         bool    `json:"replayable" yaml:"replayable"`
	Status             string  `json:"status" yaml:"status"`
	Type               string  `json:"type" yaml:"type"`
	IsReplay           bool    `json:"is_replay" yaml:"is_replay"`
	RequestMethod      string  `json:"request_method" yaml:"request_method"`
	RequestURL         string  `json:"request_url" yaml:"request_url"`
	ConnectSuccess     bool    `json:"connect_success" yaml:"connect_success"`
	ResponseStatusCode *int    `json:"response_status_code" yaml:"response_status_code"`
	ResponseTimeMs     *int    `json:"response_time_ms" yaml:"response_time_ms"`
	ErrorMessage       *string `json:"error_message" yaml:"error_message"`
	ErrorCode          *int    `json:"error_code" yaml:"error_code"`
	CreatedAt          string  `json:"created_at" yaml:"created_at"`
}

func (l Log) urlLabel() string {
	if l.Status == logStatusListen {
		return "(listen-only)"
	}
	return jsonfmt.OrDash(l.RequestURL)
}

// NewCmdWebhook builds the "sepay webhook" command group.
func NewCmdWebhook(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhook <command>",
		Aliases: []string{"wh"},
		Short:   "Webhooks, and a local stream of their events",
		Long: `A webhook is an HTTPS endpoint of yours that SePay POSTs each transaction on your
bank accounts to. "webhook listen" streams the same deliveries to a handler on your
machine, so you can build without a public URL. Webhooks are addressed by xid, the
public id "webhook list" prints.

Other products notify over channels of their own: a Payment Gateway order over
sepay pg ipn, a Bank Hub merchant's account over sepay bankhub ipn.`,
	}
	cmd.AddCommand(
		newListenCmd(f),
		newTestCmd(f),
		newListCmd(f),
		newGetCmd(f),
		newCreateCmd(f),
		newUpdateCmd(f),
		newDeleteCmd(f),
		newLogsCmd(f),
		newReplayCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnGuidance: "Writing a handler? The event body, the signature headers and what to return: https://developer.sepay.vn/en/sepay-webhooks/tich-hop-webhook.",
	})
}

func intOrDash(n *int) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(*n)
}
