package cmdutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
)

func TestExitCodeForError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ExitCode
	}{
		{"nil", nil, ExitOK},
		{"silent sentinel", ErrSilent, ExitError},
		{"silent coded", SilentError{Code: ExitAuth}, ExitAuth},
		{"wrapped coded", fmt.Errorf("context: %w", SilentError{Code: ExitOTP}), ExitOTP},
		{"flag error", &FlagError{Err: errors.New("bad flag")}, ExitUsage},
		{"api 401", &api.HTTPError{StatusCode: 401}, ExitAuth},
		{"api 403 missing scope", &api.HTTPError{StatusCode: 403, Code: "insufficient_scope"}, ExitScope},
		{"api 403 feature off", &api.HTTPError{StatusCode: 403, Code: "feature_disabled"}, ExitBlocked},
		{"api 403 no permission", &api.HTTPError{StatusCode: 403, Code: "forbidden"}, ExitBlocked},
		{"api 403 test mode off", &api.HTTPError{StatusCode: 403, Code: "test_mode_disabled"}, ExitBlocked},
		{"api 403 sandbox unprovisioned", &api.HTTPError{StatusCode: 403, Code: "sandbox_not_provisioned"}, ExitBlocked},
		{"api 401 test mode off", &api.HTTPError{StatusCode: 401, Code: "test_mode_disabled"}, ExitBlocked},
		{"api 403 unknown code", &api.HTTPError{StatusCode: 403, Code: "some_new_code"}, ExitBlocked},
		{"api otp challenge", &api.HTTPError{StatusCode: 428, Code: "otp_required"}, ExitOTP},
		{"api 422", &api.HTTPError{StatusCode: 422}, ExitError},
		{"api 500", &api.HTTPError{StatusCode: 500}, ExitError},
		{"cobra usage", errors.New(`unknown command "x" for "sepay"`), ExitUsage},
		{"generic", errors.New("boom"), ExitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExitCodeForError(tt.err))
		})
	}
}

func testFactory(format string) (*Factory, *bytes.Buffer) {
	ios, _, _, errOut := iostreams.Test()
	return &Factory{IOStreams: ios, Global: &GlobalOptions{Output: format}}, errOut
}

func TestHandleError_SilentPrintsNothing(t *testing.T) {
	f, errOut := testFactory("table")
	code := HandleError(f, SilentError{Code: ExitAuth})
	assert.Equal(t, ExitAuth, code)
	assert.Empty(t, errOut.String(), "an already-handled error prints nothing extra")
}

func TestHandleError_PrintsMessageAndUsageHint(t *testing.T) {
	f, errOut := testFactory("table")
	code := HandleError(f, &FlagError{Err: errors.New("bad flag")})
	assert.Equal(t, ExitUsage, code)
	assert.Equal(t, "Error: bad flag\nRun 'sepay --help' for usage.\n", errOut.String())
}

func TestHandleError_UsageHintNamesTheFailingCommand(t *testing.T) {
	f, errOut := testFactory("table")
	f.UsageCommand = "sepay transaction get"
	assert.Equal(t, ExitUsage, HandleError(f, &FlagError{Err: errors.New("bad flag")}))
	assert.Equal(t, "Error: bad flag\nRun 'sepay transaction get --help' for usage.\n", errOut.String())
}

func TestHandleError_JSONEnvelopeHasNoUsageHint(t *testing.T) {
	f, errOut := testFactory("json")
	f.UsageCommand = "sepay transaction get"
	HandleError(f, &FlagError{Err: errors.New("bad flag")})
	assert.JSONEq(t, `{"status":"error","error_code":"usage_error","message":"bad flag","exit_code":2}`, errOut.String())
}

func TestHandleError_JSONCarriesCodeAndExit(t *testing.T) {
	f, errOut := testFactory("json")
	code := HandleError(f, &api.HTTPError{
		StatusCode: 422,
		Code:       "validation_error",
		Message:    "Dữ liệu không hợp lệ",
		Errors:     map[string]string{"amount": "must be greater than 0"},
	})
	assert.Equal(t, ExitError, code)

	var got struct {
		Status    string            `json:"status"`
		ErrorCode string            `json:"error_code"`
		Message   string            `json:"message"`
		ExitCode  int               `json:"exit_code"`
		Errors    map[string]string `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &got))
	assert.Equal(t, "error", got.Status)
	assert.Equal(t, "validation_error", got.ErrorCode)
	assert.Equal(t, "the request failed validation", got.Message)
	assert.Equal(t, 1, got.ExitCode)
	assert.Equal(t, map[string]string{"amount": "must be greater than 0"}, got.Errors)
}

func TestHandleError_JSONNamesLocalFailures(t *testing.T) {
	f, errOut := testFactory("json")
	code := HandleError(f, &FlagError{Err: errors.New("bad flag")})
	assert.Equal(t, ExitUsage, code)

	var got struct {
		ErrorCode string `json:"error_code"`
		ExitCode  int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &got))
	assert.Equal(t, "usage_error", got.ErrorCode)
	assert.Equal(t, 2, got.ExitCode)
}

func TestHandleError_JSONEnvelopePerFailureClass(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantExit  ExitCode
		wantInMsg string
	}{
		{"usage error", &FlagError{Err: errors.New("accepts 1 arg(s), received 0")}, "usage_error", ExitUsage, "accepts 1 arg"},
		{"cobra arg count", errors.New("accepts at most 1 arg(s), received 2"), "usage_error", ExitUsage, "received 2"},
		{"unknown command", errors.New(`unknown command "bogus" for "sepay"`), "usage_error", ExitUsage, "unknown command"},
		{"not implemented", NotImplementedError{Path: "sepay report cashflow"}, "not_implemented", ExitNotImplemented, "not implemented"},
		{"unauthenticated", AuthError{Err: errors.New("not logged in, run: sepay login")}, "unauthenticated", ExitAuth, "sepay login"},
		{"scope", ScopeError{Err: errors.New("the server rejected the scope set")}, "insufficient_scope", ExitScope, "scope"},
		{"server code wins", &api.HTTPError{StatusCode: 403, Code: "forbidden"}, "forbidden", ExitBlocked, "lacks permission"},
		{"403 with no code", &api.HTTPError{StatusCode: 403, Message: "Từ chối"}, "blocked", ExitBlocked, "Từ chối"},
		{"network", api.NewTransportError("cli-api-sandbox.sepay.vn", 30*time.Second, context.DeadlineExceeded), "network_error", ExitError, "did not answer within 30s"},
		{"cancelled is not a network error", api.NewTransportError("cli-api-sandbox.sepay.vn", 30*time.Second, context.Canceled), "error", ExitError, "was cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, errOut := testFactory("json")
			assert.Equal(t, tt.wantExit, HandleError(f, tt.err))

			var got struct {
				Status    string `json:"status"`
				ErrorCode string `json:"error_code"`
				Message   string `json:"message"`
				ExitCode  int    `json:"exit_code"`
			}
			require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
			assert.Equal(t, "error", got.Status)
			assert.Equal(t, tt.wantCode, got.ErrorCode)
			assert.Equal(t, int(tt.wantExit), got.ExitCode)
			assert.Contains(t, got.Message, tt.wantInMsg)
		})
	}
}

func TestHandleError_RecoversFormatFromRawArgs(t *testing.T) {
	err := &FlagError{Err: errors.New("unknown flag: --bogus")}

	for _, args := range [][]string{
		{"transaction", "list", "--bogus", "--json"},
		{"transaction", "list", "--bogus", "-o", "json"},
	} {
		f, errOut := testFactory("table")
		f.RawArgs = args
		assert.Equal(t, ExitUsage, HandleError(f, err))
		assert.JSONEq(t, `{"status":"error","error_code":"usage_error","message":"unknown flag: --bogus","exit_code":2}`, errOut.String())
	}

	f, errOut := testFactory("table")
	f.RawArgs = []string{"transaction", "list", "--bogus"}
	assert.Equal(t, ExitUsage, HandleError(f, err))
	assert.Contains(t, errOut.String(), "Error: unknown flag: --bogus")
}

func TestMachineFormatFromArgs(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"transaction", "list"}, ""},
		{[]string{"transaction", "list", "--json"}, "json"},
		{[]string{"transaction", "list", "--json=true"}, "json"},
		{[]string{"transaction", "list", "--json=false"}, ""},
		{[]string{"-o", "json", "transaction", "list"}, "json"},
		{[]string{"transaction", "list", "-o=yaml"}, "yaml"},
		{[]string{"transaction", "list", "-oyaml"}, "yaml"},
		{[]string{"transaction", "list", "--output=json"}, "json"},
		{[]string{"transaction", "list", "--output", "table"}, ""},
		{[]string{"transaction", "list", "--json", "-o", "table"}, ""},
		{[]string{"transaction", "list", "-o", "table", "--json"}, "json"},
		{[]string{"api", "/v2/x", "--", "--json"}, ""},
		{[]string{"transaction", "list", "-o"}, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			assert.Equal(t, tt.want, machineFormatFromArgs(tt.args))
		})
	}
}

func TestFormatError_APIError(t *testing.T) {
	t.Run("known code maps to English", func(t *testing.T) {
		got := formatError(&api.HTTPError{StatusCode: 401, Code: "unauthorized", Message: "Invalid or expired token"})
		assert.Equal(t, "session expired, run: sepay login", got)
	})
	t.Run("unknown code falls back to server message", func(t *testing.T) {
		got := formatError(&api.HTTPError{StatusCode: 400, Code: "some_new_code", Message: "chi tiết lỗi từ máy chủ"})
		assert.Equal(t, "chi tiết lỗi từ máy chủ", got)
	})
	t.Run("feature_disabled maps to English", func(t *testing.T) {
		got := formatError(&api.HTTPError{StatusCode: 403, Code: "feature_disabled", Message: "Ghi trên môi trường thật qua CLI chưa được bật"})
		assert.Equal(t, "the CLI is not enabled for your company on production; ask SePay to turn it on", got)
	})
	t.Run("test_mode_disabled does not advise a re-login", func(t *testing.T) {
		got := formatError(&api.HTTPError{
			StatusCode: 403,
			Code:       "test_mode_disabled",
			Message:    "Test Mode is not enabled for this company",
		})
		assert.Equal(t, "Test Mode is off for your company, so the sandbox API refuses this; ask SePay to turn it on", got)
		assert.NotContains(t, got, "sepay login")
	})
	t.Run("sandbox_not_provisioned maps to English", func(t *testing.T) {
		got := formatError(&api.HTTPError{
			StatusCode: 403,
			Code:       "sandbox_not_provisioned",
			Message:    "The sandbox environment is not set up for this company",
		})
		assert.Equal(t, "your company has no sandbox environment yet; ask SePay to provision it", got)
		assert.NotContains(t, got, "sepay login")
	})
	t.Run("bank hub 404 maps to English", func(t *testing.T) {
		got := formatError(&api.HTTPError{
			StatusCode: 404,
			Code:       "404",
			Message:    "Giao dịch không tồn tại hoặc không thuộc merchant này",
		})
		assert.Equal(t, "not found, or not visible to this merchant", got)
	})
	t.Run("einvoice not-found codes map to English", func(t *testing.T) {
		got := formatError(&api.HTTPError{
			StatusCode: 404,
			Code:       "EINVOICE_DOCUMENT_NOT_FOUND",
			Message:    "Không tìm thấy hóa đơn",
		})
		assert.Equal(t, "no invoice with that reference code", got)
	})
	t.Run("not_found keeps the server's sandbox-only message", func(t *testing.T) {
		got := formatError(&api.HTTPError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    "This endpoint is only available in the sandbox environment",
		})
		assert.Equal(t, "This endpoint is only available in the sandbox environment", got)
	})
	t.Run("validation errors list each field", func(t *testing.T) {
		got := formatError(&api.HTTPError{
			StatusCode: 422,
			Code:       "validation_error",
			Message:    "The provided data failed validation",
			Errors:     map[string]string{"url": "url is required", "bank_account_xids": "at least one account"},
		})
		assert.Contains(t, got, "the request failed validation")
		assert.Contains(t, got, "\n  - bank_account_xids: at least one account")
		assert.Contains(t, got, "\n  - url: url is required")
	})
	t.Run("non-api error renders verbatim", func(t *testing.T) {
		assert.Equal(t, "boom", formatError(errors.New("boom")))
	})
}

func TestErrorMessage_MapsProductBrokerCodes(t *testing.T) {
	const generic = "An unexpected error occurred, please contact SePay support"
	for _, code := range []string{
		"pg_provision_failed", "bankhub_provision_failed", "einvoice_provision_failed",
		"bankhub_token_failed", "einvoice_token_failed", "product_token_failed",
		"live_company_unresolved",
	} {
		t.Run(code, func(t *testing.T) {
			err := &api.HTTPError{StatusCode: 500, Code: code, Message: generic}
			got := errorMessage(err)
			assert.NotEqual(t, generic, got, "the server message must not survive as the answer")
			assert.NotEmpty(t, got)
		})
	}
}

func TestErrorMessage_UnknownCodeKeepsServerMessage(t *testing.T) {
	err := &api.HTTPError{StatusCode: 422, Code: "some_new_code", Message: "server says this"}
	assert.Equal(t, "server says this", errorMessage(err))
}

func TestErrorMessage_ServerErrorsAreDefined(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     *api.HTTPError
		want    []string
		notWant []string
	}{
		{
			"bare 500", &api.HTTPError{StatusCode: 500},
			[]string{"SePay had an internal error (HTTP 500)", "contact SePay support"},
			[]string{"()", "API error"},
		},
		{
			"500 with code and request id",
			&api.HTTPError{StatusCode: 500, Code: "webhook_persist_failed", Message: "Đã có lỗi xảy ra, vui lòng thử lại sau.", RequestID: "req_abc123"},
			[]string{"(code webhook_persist_failed, request id req_abc123)"},
			[]string{"Đã có lỗi"},
		},
		{
			"internal detail is not relayed",
			&api.HTTPError{StatusCode: 500, Message: "SQLSTATE[42S22]: Unknown column 'xid' in field list"},
			[]string{"internal error (HTTP 500)"},
			[]string{"SQLSTATE", "xid"},
		},
		{
			"proxy 502", &api.HTTPError{StatusCode: 502, Message: "Bad Gateway"},
			[]string{"SePay is temporarily unavailable (HTTP 502); retry in a moment"},
			[]string{"Bad Gateway"},
		},
		{
			"503 with request id", &api.HTTPError{StatusCode: 503, Code: "sse_hub_unavailable", RequestID: "r1"},
			[]string{"temporarily unavailable (HTTP 503)", "request id r1"},
			nil,
		},
		{
			"mapped 5xx code keeps its own line", &api.HTTPError{StatusCode: 503, Code: "sse_hub_disabled", Message: "x"},
			[]string{"switched off on SePay's side"},
			[]string{"retry in a moment"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := errorMessage(tc.err)
			for _, w := range tc.want {
				assert.Contains(t, got, w)
			}
			for _, w := range tc.notWant {
				assert.NotContains(t, got, w)
			}
		})
	}
}

func TestErrorMessage_EmptyClientErrorIsDefined(t *testing.T) {
	got := errorMessage(&api.HTTPError{StatusCode: 404})
	assert.Equal(t, "unexpected response from SePay (HTTP 404)", got)
	assert.Equal(t, "API error 404", (&api.HTTPError{StatusCode: 404}).Error(), "no empty () in the wrapped form either")
}

func TestHandleError_ServerErrorEnvelope(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "json"}}
	code := HandleError(f, &api.HTTPError{StatusCode: 500, Message: "<html>oops</html>", RequestID: "req_1"})
	assert.Equal(t, ExitError, code)
	var env map[string]any
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &env), errOut.String())
	assert.Equal(t, "server_error", env["error_code"])
	assert.Equal(t, "req_1", env["request_id"])
	assert.NotContains(t, env["message"], "oops")
}

func TestErrorMessage_MapsSandboxBankPlanCodes(t *testing.T) {
	for code, want := range map[string]string{
		"sms_not_allowed":          "use --connection api",
		"connection_not_supported": "this bank has no API connection",
	} {
		err := &api.HTTPError{StatusCode: 422, Code: code, Message: "server text"}
		assert.Contains(t, errorMessage(err), want, code)
		assert.Equal(t, ExitError, ExitCodeForError(err), code)
	}
}

func TestHandleError_InterruptedPrintsNothing(t *testing.T) {
	f, errOut := testFactory("table")
	assert.Equal(t, ExitInterrupted, HandleError(f, ErrInterrupted))
	assert.Empty(t, errOut.String())
}

func TestHandleError_InterruptedJSONEnvelope(t *testing.T) {
	f, errOut := testFactory("json")
	assert.Equal(t, ExitInterrupted, HandleError(f, fmt.Errorf("prompt: %w", ErrInterrupted)))

	var got struct {
		ErrorCode string `json:"error_code"`
		ExitCode  int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &got))
	assert.Equal(t, "interrupted", got.ErrorCode)
	assert.Equal(t, 130, got.ExitCode)
}

// The server words these in Vietnamese; the CLI speaks English and says what to do next.
func TestErrorMessage_PaycodeConflictsAreEnglish(t *testing.T) {
	for code, want := range map[string]string{
		"paycode_prefix_exists":    "sepay paycode update --structure <prefix>",
		"paycode_version_conflict": "nothing was saved, run the command again",
	} {
		got := errorMessage(&api.HTTPError{StatusCode: 409, Code: code, Message: "Cấu hình mã thanh toán"})
		assert.Contains(t, got, want, code)
		assert.NotContains(t, got, "Cấu hình", code)
	}
}

// `sepay help errors` documents what HandleError does, so it must name every code the CLI rewords
// and give the exit code ExitCodeForError returns for the codes that set their own.
func TestErrorsTopic_MatchesTheCodesTheCLIHandles(t *testing.T) {
	raw, err := os.ReadFile("../cmd/root/topics/errors.md")
	require.NoError(t, err)
	topic := string(raw)

	for code := range apiErrorMessage {
		assert.Contains(t, topic, "`"+code+"`", "the errors topic does not name %s", code)
	}

	ownExit := []string{
		"insufficient_scope", "access_revoked", "otp_required", "feature_disabled", "forbidden",
		"test_mode_disabled", "sandbox_not_provisioned", "sse_hub_disabled", "quota_exceeded",
	}
	for _, code := range ownExit {
		want := ExitCodeForError(&api.HTTPError{StatusCode: 403, Code: code})
		row := fmt.Sprintf("| %#q | %d |", code, want)
		assert.Contains(t, topic, row, "the errors topic gives %s another exit code", code)
	}
}
