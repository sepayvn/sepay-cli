package api

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DebugTransport logs each request at debug level. Only header names are logged and query values
// are masked unless listed in unmaskedParams.
func DebugTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &debugTransport{base: base}
}

// unmaskedParams is an allowlist so a new filter param is masked by default.
var unmaskedParams = map[string]bool{
	"page": true, "per_page": true, "sort": true, "order": true,
	"env": true, "source": true, "since": true,
	"status": true, "order_status": true, "event_type": true,
	"transfer_type": true, "bank_id": true,
	"start_transaction_date": true, "end_transaction_date": true,
	"from_created_at": true, "to_created_at": true,
}

type debugTransport struct{ base http.RoundTripper }

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !slog.Default().Enabled(req.Context(), slog.LevelDebug) {
		return t.base.RoundTrip(req)
	}

	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		names = append(names, name)
	}
	url := debugURL(req.URL)
	slog.Debug("request", "method", req.Method, "url", url, "headers", names)

	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		slog.Debug("request failed", "method", req.Method, "url", url, "duration", elapsed, "error", err)
		return resp, err
	}
	slog.Debug("response", "method", req.Method, "url", url, "status", resp.StatusCode, "duration", elapsed)
	return resp, nil
}

func debugURL(u *url.URL) string {
	q := u.Query()
	masked := false
	for param, values := range q {
		if unmaskedParams[strings.ToLower(param)] {
			continue
		}
		for i, v := range values {
			values[i] = maskTail(v)
		}
		masked = true
	}
	if !masked {
		return u.Redacted()
	}
	clone := *u
	// Encode escapes the mask's asterisks.
	clone.RawQuery = strings.ReplaceAll(q.Encode(), "%2A", "*")
	return clone.Redacted()
}

func maskTail(v string) string {
	if len(v) <= 4 {
		return "****"
	}
	return "****" + v[len(v)-4:]
}
