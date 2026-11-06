package cmdutil

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
)

// apiErrorMessage maps known API error_codes to English messages.
var apiErrorMessage = map[string]string{ //nolint:gosec // G101: error messages, no credential
	"unauthorized":             "session expired, run: sepay login",
	"insufficient_scope":       "token lacks the required scope, run: sepay auth refresh --scopes <scope>",
	"rate_limited":             "too many requests, retry shortly",
	"validation_error":         "the request failed validation",
	"feature_disabled":         "the CLI is not enabled for your company on production; ask SePay to turn it on",
	"test_mode_disabled":       "Test Mode is off for your company, so the sandbox API refuses this; ask SePay to turn it on",
	"sandbox_not_provisioned":  "your company has no sandbox environment yet; ask SePay to provision it",
	"forbidden":                "your account lacks permission for this action; ask an admin of your company",
	"multiple_companies":       "your account belongs to more than one company, which the CLI cannot act for",
	"sms_not_allowed":          "your plan does not include SMS connections; use --connection api",
	"connection_not_supported": "this bank has no API connection; pick another bank, or use --connection sms if your plan includes SMS",
	"otp_required":             "this action needs an OTP, which the CLI cannot supply yet; do it in the web app",
	"unsupported_api_version":  "this sepay CLI version is not supported by the server; upgrade to the latest release",
	"access_revoked":           "your access to this stream was revoked, run: sepay login",
	"sse_hub_disabled":         "the listen feature is switched off on SePay's side; it works again once SePay turns it back on",
	"replay_not_dispatched":    "the replay was not sent",
	"replay_already_delivered": "the last delivery of this transaction to this webhook already succeeded; replay it anyway with --force",
	"replay_rate_limited":      "this transaction reached its replay limit",
	"replay_busy":              "another replay of this transaction is in progress; retry shortly",
	"paycode_prefix_exists":    "this company already has a pay-code structure with that prefix (letter case aside); change it with: sepay paycode update --structure <prefix>",
	"paycode_version_conflict": "the pay-code configuration changed since it was read; nothing was saved, run the command again (with a new --idempotency-key if you passed one)",

	// Product broker: all answer 500 with one generic message, so only error_code tells the steps apart.
	"pg_provision_failed":       "sandbox Payment Gateway provisioning failed server-side; retry, then report it to SePay",
	"bankhub_provision_failed":  "sandbox Bank Hub provisioning failed server-side; retry, then report it to SePay",
	"einvoice_provision_failed": "sandbox e-invoice provisioning failed server-side; retry, then report it to SePay",
	"bankhub_token_failed":      "the sandbox Bank Hub merchant exists but SePay could not issue a token; retry, then report it",
	"einvoice_token_failed":     "the sandbox e-invoice credential exists but SePay could not issue a token; retry, then report it",
	"product_token_failed":      "SePay could not issue this product credential; retry, then report it to SePay",
	"live_company_unresolved":   "SePay could not resolve your company for this request; run: sepay login",

	// Bank Hub sends the HTTP status as its code. "not_found" is absent on purpose: api/v2 uses it
	// for production-gated endpoints, where the server message is more useful.
	"404":                         "not found, or not visible to this merchant",
	"EINVOICE_DOCUMENT_NOT_FOUND": "no invoice with that reference code",
	"EINVOICE_ACCOUNT_NOT_FOUND":  "this company has no e-invoice provider account",

	// Payment Gateway answers in Vietnamese; the numeric code is the stable key.
	"4000": "the order has not been paid, so there is nothing to void",
	"4001": "the order was already cancelled",
	"4002": "the order has no payment transaction",
}

// reasonCodes are codes whose server message is appended as the reason.
var reasonCodes = map[string]bool{"replay_not_dispatched": true, "replay_already_delivered": true, "replay_rate_limited": true}

func errorMessage(err error) string {
	var apiErr *api.HTTPError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	if msg := apiErrorMessage[apiErr.Code]; msg != "" {
		if reasonCodes[apiErr.Code] && apiErr.Message != "" {
			return msg + " (Reason: " + apiErr.Message + ")"
		}
		return msg
	}
	// A 5xx message is not relayed (internal text, HTML, generic Vietnamese); code and request id are kept.
	if apiErr.StatusCode >= 500 {
		return serverErrorMessage(apiErr)
	}
	if apiErr.Message != "" {
		return apiErr.Message
	}
	return fmt.Sprintf("unexpected response from SePay (HTTP %d)", apiErr.StatusCode)
}

func serverErrorMessage(e *api.HTTPError) string {
	var msg string
	switch e.StatusCode {
	case 502, 503, 504:
		msg = fmt.Sprintf("SePay is temporarily unavailable (HTTP %d); retry in a moment", e.StatusCode)
	default:
		msg = fmt.Sprintf("SePay had an internal error (HTTP %d); retry in a moment, and if it keeps failing contact SePay support", e.StatusCode)
	}
	var ref []string
	if e.Code != "" {
		ref = append(ref, "code "+e.Code)
	}
	if e.RequestID != "" {
		ref = append(ref, "request id "+e.RequestID)
	}
	if len(ref) > 0 {
		msg += " (" + strings.Join(ref, ", ") + ")"
	}
	return msg
}

// formatError is errorMessage plus field-level validation errors.
func formatError(err error) string {
	msg := errorMessage(err)
	var apiErr *api.HTTPError
	if !errors.As(err, &apiErr) || len(apiErr.Errors) == 0 {
		return msg
	}
	fields := make([]string, 0, len(apiErr.Errors))
	for k := range apiErr.Errors {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	var b strings.Builder
	b.WriteString(msg)
	for _, k := range fields {
		Fprintf(&b, "\n  - %s: %s", k, apiErr.Errors[k])
	}
	return b.String()
}

func cleanKeepingNewlines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = jsonfmt.Clean(l)
	}
	return strings.Join(lines, "\n")
}

// ExitCoder lets an error declare its own exit code.
type ExitCoder interface {
	ExitCode() ExitCode
}

// FlagError marks a misused flag/argument as a usage error.
type FlagError struct {
	Err error
}

func (e *FlagError) Error() string { return e.Err.Error() }
func (e *FlagError) Unwrap() error { return e.Err }

// ExitCode returns ExitUsage.
func (e *FlagError) ExitCode() ExitCode { return ExitUsage }

// ServerValueError re-reports a validator's FlagError on a server-supplied value as an ordinary error,
// so the caller is not blamed with exit 2.
func ServerValueError(what string, err error) error {
	var flagErr *FlagError
	if errors.As(err, &flagErr) {
		return fmt.Errorf("%s: %w", what, flagErr.Err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// UnknownProfileError reports a named profile with no stored entry, as a usage error.
func UnknownProfileError(name string) error {
	return &FlagError{Err: fmt.Errorf("unknown profile %q (see: sepay auth status)", name)}
}

// ScopeError marks a failure caused by the token's scopes.
type ScopeError struct {
	Err error
}

func (e ScopeError) Error() string { return e.Err.Error() }
func (e ScopeError) Unwrap() error { return e.Err }

// ExitCode returns ExitScope.
func (e ScopeError) ExitCode() ExitCode { return ExitScope }

// AuthError marks a failure caused by having no usable credential.
type AuthError struct {
	Err error
}

func (e AuthError) Error() string { return e.Err.Error() }
func (e AuthError) Unwrap() error { return e.Err }

// ExitCode returns ExitAuth.
func (e AuthError) ExitCode() ExitCode { return ExitAuth }

// ErrSilent signals "already printed": the caller only sets the exit code.
var ErrSilent = errors.New("silent error")

// SilentError is an already-printed error carrying only its exit code.
type SilentError struct{ Code ExitCode }

func (SilentError) Error() string { return "" }

// ExitCode returns the carried exit code.
func (e SilentError) ExitCode() ExitCode { return e.Code }

// ExitCodeForError classifies err into an ExitCode without printing anything.
func ExitCodeForError(err error) ExitCode {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrSilent):
		return ExitError
	}

	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}

	var apiErr *api.HTTPError
	if errors.As(err, &apiErr) {
		// A 403 means several things; only a missing scope is fixable by the caller.
		switch apiErr.Code {
		case "insufficient_scope":
			return ExitScope
		case "access_revoked":
			return ExitAuth
		case "otp_required":
			return ExitOTP
		case "feature_disabled", "forbidden", "test_mode_disabled", "sandbox_not_provisioned",
			"sse_hub_disabled":
			return ExitBlocked
		case "quota_exceeded":
			// Not ExitBlocked: the caller clears their own quota.
			return ExitError
		}
		switch apiErr.StatusCode {
		case 401:
			return ExitAuth
		case 403:
			return ExitBlocked
		}
		return ExitError
	}

	if isCobraUsageError(err) {
		return ExitUsage
	}
	return ExitError
}

// ErrInterrupted reports that the run was stopped: Ctrl+C or Esc at a prompt, or SIGINT/SIGTERM.
var ErrInterrupted error = interruptedError{}

type interruptedError struct{}

func (interruptedError) Error() string { return "interrupted" }

func (interruptedError) ExitCode() ExitCode { return ExitInterrupted }

// errorOutput mirrors the API's error envelope.
type errorOutput struct {
	Status    string            `json:"status" yaml:"status"`
	ErrorCode string            `json:"error_code" yaml:"error_code"`
	Message   string            `json:"message" yaml:"message"`
	ExitCode  ExitCode          `json:"exit_code" yaml:"exit_code"`
	RequestID string            `json:"request_id,omitempty" yaml:"request_id,omitempty"`
	Errors    map[string]string `json:"errors,omitempty" yaml:"errors,omitempty"`
}

// HandleError prints err to stderr (errorOutput under machine output) and returns its exit code.
func HandleError(f *Factory, err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, ErrSilent) {
		return ExitError
	}
	var se SilentError
	if errors.As(err, &se) {
		return se.Code
	}

	code := ExitCodeForError(err)
	ios := f.IOStreams
	switch f.outputFormat() {
	case "json":
		return renderErrorOutput(jsonfmt.RenderJSON, ios.ErrOut, code, err)
	case "yaml":
		return renderErrorOutput(jsonfmt.RenderYAML, ios.ErrOut, code, err)
	}

	// Ctrl+C, Esc or a stop signal: whoever stopped the run asked for it, so nothing is printed.
	if code == ExitInterrupted {
		return code
	}
	// Errors embed user paths and server strings: sanitise line by line to keep multi-line validation output.
	fmt.Fprintln(ios.ErrOut, "Error:", cleanKeepingNewlines(formatError(err)))
	if code == ExitUsage {
		path := f.UsageCommand
		if path == "" {
			path = "sepay"
		}
		fmt.Fprintf(ios.ErrOut, "Run '%s --help' for usage.\n", path)
	}
	return code
}

// A parse-level failure can leave Global at its default, so fall back to the raw args.
func (f *Factory) outputFormat() string {
	if f.Global == nil {
		return ""
	}
	switch f.Global.Output {
	case "json", "yaml":
		return f.Global.Output
	}
	if format := machineFormatFromArgs(f.RawArgs); format != "" {
		return format
	}
	return f.Global.Output
}

// machineFormatFromArgs returns "json" or "yaml" from raw args; last wins, "--" ends the scan.
func machineFormatFromArgs(args []string) string {
	format := ""
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		a := args[i]
		switch {
		case a == "--json":
			format = "json"
		case strings.HasPrefix(a, "--json="):
			if on, err := strconv.ParseBool(strings.TrimPrefix(a, "--json=")); err == nil && on {
				format = "json"
			}
		case a == "-o" || a == "--output":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--output="):
			format = strings.TrimPrefix(a, "--output=")
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			format = strings.TrimPrefix(a[2:], "=")
		}
	}
	switch format {
	case "json", "yaml":
		return format
	}
	return ""
}

func renderErrorOutput(render func(io.Writer, any) error, w io.Writer, code ExitCode, err error) ExitCode {
	out := errorOutput{
		Status:    "error",
		ErrorCode: errorCodes[code],
		Message:   errorMessage(err),
		ExitCode:  code,
	}
	var transportErr *api.TransportError
	if errors.As(err, &transportErr) && !transportErr.Cancelled() {
		out.ErrorCode = "network_error"
	}
	var apiErr *api.HTTPError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code != "":
			out.ErrorCode = apiErr.Code
		case apiErr.StatusCode >= 500:
			out.ErrorCode = "server_error"
		}
		out.RequestID = apiErr.RequestID
		out.Errors = apiErr.Errors
	}
	if renderErr := render(w, out); renderErr != nil {
		fmt.Fprintln(w, "Error:", formatError(err))
	}
	return code
}

// isCobraUsageError matches Cobra's stable usage messages; Cobra has no typed error for them.
func isCobraUsageError(err error) bool {
	msg := err.Error()
	for _, p := range []string{
		"unknown command",
		"unknown subcommand",
		"accepts ",
		"requires ",
		"invalid argument",
		"required flag",
	} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}
