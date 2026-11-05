package listen

import (
	"net/http"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/forward"
)

// credentialHeaders are replayed verbatim, never printed; a substring net catches new auth types.
var credentialHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"X-Secret-Key":        true,
	"X-Api-Key":           true,
	"Api-Key":             true,
	"Cookie":              true,
}

// isCredentialHeader excludes signatures: the handler needs them to verify.
func isCredentialHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	if credentialHeaders[canonical] {
		return true
	}
	lower := strings.ToLower(canonical)
	for _, needle := range []string{"secret", "token", "password", "credential"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func displayHeaderValue(name, value string) string {
	if isCredentialHeader(name) {
		return "<redacted>"
	}
	return value
}

func withholdCredentials(h http.Header) {
	for name := range h {
		if isCredentialHeader(name) {
			h.Del(name)
		}
	}
}

// WithholdCredentialsFor reports whether credential headers must be dropped for fwd: any
// non-loopback target (https proves secrecy, not identity) unless --forward-credentials, and
// always for an unverified certificate. Signature headers are never withheld.
func WithholdCredentialsFor(fwd *forward.Forwarder, forwardCredentials bool) bool {
	if fwd.IsUnverifiedTLS() {
		return true
	}
	if fwd.IsLoopback() {
		return false
	}
	// Plaintext off-machine stays refused even with the opt-in.
	if fwd.IsPlaintextRemote() {
		return true
	}
	return !forwardCredentials
}

// WithholdReason names why credentials are withheld, without suggesting a flag that cannot help.
func WithholdReason(fwd *forward.Forwarder) string {
	if fwd.IsUnverifiedTLS() {
		return "withholding credential headers (Authorization, X-Secret-Key) while TLS verification is off; use a trusted certificate or plain http on loopback to replay them"
	}
	if fwd.IsPlaintextRemote() {
		return "withholding credential headers (Authorization, X-Secret-Key) over plaintext http; use https or a loopback address to replay them"
	}
	return "withholding credential headers (Authorization, X-Secret-Key) from a non-loopback handler; pass --forward-credentials to replay them"
}

// OutboundHeaders returns fr's headers to replay, minus credentials when withhold is set.
func OutboundHeaders(fr Frame, withhold bool) http.Header {
	headers := DeliveryHeaders(fr)
	if withhold {
		withholdCredentials(headers)
	}
	return headers
}

// DeliveryHeaders returns a frame's headers minus hop-by-hop ones, defaulting Content-Type to JSON.
func DeliveryHeaders(frame Frame) http.Header {
	headers := http.Header{}
	for k, v := range frame.Headers {
		if isHopByHopHeader(k) {
			continue
		}
		headers.Set(k, v)
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	return headers
}

func isHopByHopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Host", "Content-Length", "Connection", "Keep-Alive", "Proxy-Connection",
		"Transfer-Encoding", "Te", "Trailer", "Upgrade":
		return true
	}
	return false
}
