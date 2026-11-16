package webhook

import (
	"encoding/json"
	"maps"
)

// Stored forms of the write enums: what get returns for each value a write sends.
var (
	storedEventType   = map[string]string{"all": "All", "in": "In_only", "out": "Out_only"}
	storedAuthType    = map[string]string{"none": "No_Authen", "apikey": "Api_Key", "hmac": "HMAC_SHA256", "oauth2": "OAuth2.0"}
	storedContentType = map[string]string{"json": "Json", "form": "application_x-www-form-urlencoded", "multipart": "multipart_form-data"}
)

// paymentSetupNotChecked is the extra not_checked line of a create or update dry run: After does not
// derive the targeting the server stores, and the CLI reads accounts and VAs only for some flags.
const paymentSetupNotChecked = "payment receiving setup (bank accounts, VAs, bank_config, only_va): not checked by the CLI; the server checks it and may change or refuse it"

// secretKeys are the body keys that carry a credential.
var secretKeys = []string{"secret", "oauth2_client_secret"}

// maskedBody returns body with each credential it sets as "***", for a dry run to print. An empty
// one stays empty: it sets nothing, and "***" would read as a secret about to be stored.
func maskedBody(body map[string]any) map[string]any {
	out := maps.Clone(body)
	for _, k := range secretKeys {
		if v, _ := out[k].(string); v != "" {
			out[k] = redactedMark
		}
	}
	return out
}

// updatePreview is w with the fields a write body sends, as sent, in get's field names and stored
// enum values, redacted like get. Nothing the server derives from them is guessed into after.
type updatePreview struct {
	after    Webhook
	replaced []string // masked fields the write sets
}

// previewCreate is the fields a create body sends, as get names them; the rest is the server's.
func previewCreate(body map[string]any) (map[string]any, error) {
	p := previewUpdate(Webhook{}, body)
	// #nosec G117 -- applyCredentials sets every secret field to "***"; Before is empty
	raw, err := json.Marshal(p.after)
	if err != nil {
		return nil, err
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	sent := map[string]any{}
	for key := range body {
		for _, field := range getFields[key] {
			if v, ok := all[field]; ok {
				sent[field] = v
			}
		}
	}
	return sent, nil
}

// getFields are the get fields each body key sets.
var getFields = map[string][]string{
	"name": {"name"}, "url": {"webhook_url"}, "delivery_mode": {"delivery_mode"}, "event_type": {"event_type"},
	"auth_type": {"authen_type"}, "content_type": {"request_content_type"},
	"secret": {"api_key", "secret_key"}, "oauth2_client_id": {"oauth2_client_id"},
	"oauth2_client_secret": {"oauth2_client_secret"}, "oauth2_access_token_url": {"oauth2_access_token_url"},
	"is_verify_payment": {"is_verify_payment"}, "skip_if_no_code": {"skip_if_no_code"}, "prefix_filters": {"prefix_filters"},
	"bank_mode": {"bank_mode"}, "bank_account_xid": {"bank_account_xid"}, "bank_account_xids": {"bank_account_xids"},
	"bank_config": {"bank_config"}, "bank_sub_account_xids": {"bank_sub_account_xids"}, "only_va": {"only_va"},
}

func previewUpdate(w Webhook, body map[string]any) updatePreview {
	p := updatePreview{after: w}
	a := &p.after
	str := func(key string) (string, bool) {
		v, ok := body[key].(string)
		return v, ok
	}
	if v, ok := str("name"); ok {
		a.Name = v
	}
	if v, ok := str("url"); ok {
		a.WebhookURL = v
	}
	if v, ok := str("delivery_mode"); ok {
		a.DeliveryMode = v
	}
	if v, ok := str("event_type"); ok {
		a.EventType = storedEventType[v]
	}
	if v, ok := body["is_verify_payment"].(bool); ok {
		a.IsVerifyPayment = v
	}
	if v, ok := body["skip_if_no_code"].(bool); ok {
		a.SkipIfNoCode = v
	}
	if v, ok := body["prefix_filters"].([]string); ok {
		a.PrefixFilters = v
	}
	if v, ok := str("auth_type"); ok {
		a.AuthenType = storedAuthType[v]
	}
	if v, ok := str("content_type"); ok {
		a.RequestContentType = storedContentType[v]
	}
	p.applyCredentials(body)

	if v, ok := str("bank_mode"); ok {
		a.BankMode = v
	}
	if v, ok := str("bank_account_xid"); ok {
		a.BankAccountXID = &v
	}
	if v, ok := body["bank_account_xids"].([]string); ok {
		a.BankAccountXIDs = v
	}
	// bank_config is a patch keyed by account xid: the server keeps each stored entry the body
	// leaves out (pickBankConfig), and the CLI leaves unchanged accounts out on purpose.
	if cfg, ok := body["bank_config"].(map[string]any); ok {
		a.BankConfig = maps.Clone(w.BankConfig)
		if a.BankConfig == nil {
			a.BankConfig = make(map[string]BankConfigEntry, len(cfg))
		}
		for xid, entry := range cfg {
			a.BankConfig[xid] = entryOf(entry)
		}
	}
	if v, ok := body["bank_sub_account_xids"].([]string); ok {
		a.SubAccountXIDs = v
	}
	if v, ok := body["only_va"].(bool); ok {
		a.OnlyVA = v
	}
	return p
}

// applyCredentials sets the credential fields the body sends, each secret as "***". get names the
// "secret" field after the scheme sent with it: api_key for apikey, secret_key for hmac.
func (p *updatePreview) applyCredentials(body map[string]any) {
	a := &p.after
	if v, ok := body["oauth2_client_id"].(string); ok {
		a.OAuth2ClientID = v
	}
	if v, ok := body["oauth2_access_token_url"].(string); ok {
		a.OAuth2AccessTokenURL = v
	}
	if v, _ := body["oauth2_client_secret"].(string); v != "" {
		a.OAuth2ClientSecret = redactedMark
		p.replaced = append(p.replaced, "oauth2_client_secret")
	}
	if v, _ := body["secret"].(string); v == "" {
		return
	}
	switch a.AuthenType {
	case storedAuthType["apikey"]:
		a.APIKey = redactedMark
		p.replaced = append(p.replaced, "api_key")
	case storedAuthType["hmac"]:
		a.SecretKey = redactedMark
		p.replaced = append(p.replaced, "secret_key")
	}
}
