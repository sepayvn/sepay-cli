package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func TestAPI_GetPrintsResponse(t *testing.T) {
	var method, path, query string
	f, out := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1"},"meta":{"pagination":{"total":1,"per_page":5,"current_page":1,"last_page":1,"has_more":false}}}`))
	})
	require.NoError(t, run(NewCmdAPI(f), "/transactions", "-f", "per_page=5", "-f", "q=abc"))
	assert.Equal(t, http.MethodGet, method)
	assert.Equal(t, "/transactions", path)
	assert.Contains(t, query, "per_page=5")
	assert.Contains(t, query, "q=abc")
	assert.Contains(t, out.String(), `"id": "wh_1"`)
	assert.Contains(t, out.String(), `"total": 1`)
}

func TestAPI_YAMLKeepsIntegersIntact(t *testing.T) {
	f, out := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"amount":1000000,"big":9007199254740993,"rate":2.5}}`))
	})
	f.Global.Output = "yaml"
	require.NoError(t, run(NewCmdAPI(f), "/transactions"))
	assert.Contains(t, out.String(), "amount: 1000000")
	assert.Contains(t, out.String(), "big: 9007199254740993")
	assert.Contains(t, out.String(), "rate: 2.5")
	assert.NotContains(t, out.String(), "e+06")
}

func TestAPI_PathNormalizedToLeadingSlash(t *testing.T) {
	var path string
	f, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	require.NoError(t, run(NewCmdAPI(f), "transactions"))
	assert.Equal(t, "/transactions", path)
}

func TestAPI_PostFieldsBecomeTypedJSONBody(t *testing.T) {
	var method, idem string
	var body map[string]any
	f, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		idem = r.Header.Get("X-Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9"}}`))
	})
	require.NoError(t, run(NewCmdAPI(f), "/webhooks", "-X", "POST",
		"-f", "url=https://example.com/hook", "-f", "is_verify_payment=true", "-f", "count=5"))
	assert.Equal(t, http.MethodPost, method)
	assert.NotEmpty(t, idem, "a mutation must carry an idempotency key")
	assert.Equal(t, "https://example.com/hook", body["url"]) // non-JSON value stays a string
	assert.Equal(t, true, body["is_verify_payment"])         // JSON value is sent typed
	assert.Equal(t, float64(5), body["count"])
}

func TestAPI_InputRawBody(t *testing.T) {
	var body map[string]any
	f, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	f.IOStreams.In = io.NopCloser(strings.NewReader(`{"url":"https://x.test","only_va":true}`))
	require.NoError(t, run(NewCmdAPI(f), "/webhooks", "-X", "POST", "--input", "-"))
	assert.Equal(t, "https://x.test", body["url"])
	assert.Equal(t, true, body["only_va"])
}

func TestAPI_CustomHeaderSent(t *testing.T) {
	var got string
	f, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Trace-Id")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	require.NoError(t, run(NewCmdAPI(f), "/transactions", "-H", "X-Trace-Id: abc123"))
	assert.Equal(t, "abc123", got)
}

func TestAPI_ServerErrorSurfaces(t *testing.T) {
	f, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"not_found","message":"no"}`))
	})
	err := run(NewCmdAPI(f), "/nope")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

func TestAPI_UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		in   string
	}{
		{"input with GET", []string{"/x", "--input", "-"}, "{}"},
		{"input and field together", []string{"/x", "-X", "POST", "--input", "-", "-f", "a=1"}, "{}"},
		{"malformed field", []string{"/x", "-X", "POST", "-f", "noeq"}, ""},
		{"malformed header", []string{"/x", "-H", "bogus"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not reach the server on a usage error")
			})
			if tc.in != "" {
				f.IOStreams.In = io.NopCloser(strings.NewReader(tc.in))
			}
			err := run(NewCmdAPI(f), tc.args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestAPI_InvalidInputJSON(t *testing.T) {
	f, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not send an invalid body")
	})
	f.IOStreams.In = io.NopCloser(strings.NewReader(`not json`))
	err := run(NewCmdAPI(f), "/x", "-X", "POST", "--input", "-")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestRejectAbsoluteURL(t *testing.T) {
	const base = "http://127.0.0.1:8079/api/v2"
	cases := []struct {
		name, arg, wantSuggestion string
		wantErr                   bool
	}{
		// A local base repeats only "/v2"; the suggestion must pass rejectDuplicateBasePath.
		{name: "https URL", arg: "https://cli-api.sepay.vn/v2/transactions", wantErr: true, wantSuggestion: "/transactions"},
		{name: "URL carrying the base's own prefix", arg: "http://127.0.0.1:8079/api/v2/transactions", wantErr: true, wantSuggestion: "/transactions"},
		{name: "the query survives into the suggestion", arg: "https://host/transactions?per_page=1", wantErr: true, wantSuggestion: "/transactions?per_page=1"},
		{name: "network-path reference", arg: "//evil.example.com/steal", wantErr: true, wantSuggestion: "/steal"},
		{name: "host only, nothing to keep", arg: "https://cli-api.sepay.vn", wantErr: true, wantSuggestion: "/transactions"},
		{name: "URL in a query value", arg: "/webhooks?url=https://example.com/hook"},
		{name: "URL in a query value, no leading slash", arg: "webhooks?url=https://example.com/hook"},
		{name: "ordinary path", arg: "/transactions"},
		{name: "path with a colon", arg: "/transactions/tx:1"},
		{name: "single slash", arg: "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectAbsoluteURL(base, tc.arg)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), "a URL where a path belongs is a usage error")
			assert.Contains(t, err.Error(), tc.wantSuggestion)
		})
	}
}

func TestRejectDotSegments(t *testing.T) {
	const base = "http://127.0.0.1:8079/api/v2"
	cases := []struct {
		name, path, wantSuggestion string
		wantErr                    bool
	}{
		// The server resolves this to /oauth/tokens.
		{name: "walks out through a real segment", path: "/x/../../../oauth/tokens", wantErr: true, wantSuggestion: "/oauth/tokens"},
		{name: "walks out from the root", path: "/../../../oauth/tokens", wantErr: true, wantSuggestion: "/oauth/tokens"},
		{name: "percent-encoded", path: "/%2e%2e/%2e%2e/oauth/tokens", wantErr: true, wantSuggestion: "/oauth/tokens"},
		{name: "re-enters the base", path: "/banks/../../v2/banks", wantErr: true, wantSuggestion: "use /banks"},
		{name: "single dot", path: "/./transactions", wantErr: true, wantSuggestion: "/transactions"},
		{name: "the query survives into the suggestion", path: "/x/../transactions?per_page=1", wantErr: true, wantSuggestion: "/transactions?per_page=1"},
		{name: "dotted file-like segment", path: "/reports/cashflow.csv"},
		{name: "dots inside a segment", path: "/transactions/..tx"},
		{name: "dot segments in a query value", path: "/webhooks?url=https://example.com/a/../b"},
		{name: "ordinary path", path: "/transactions"},
		{name: "root", path: "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectDotSegments(base, tc.path)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), "a path that leaves the base is a usage error")
			assert.Contains(t, err.Error(), tc.wantSuggestion)
		})
	}
}

func TestRejectDotSegments_SuggestionPassesTheOtherGuards(t *testing.T) {
	const base = "http://127.0.0.1:8079/api/v2"
	for _, p := range []string{
		"/x/../../../oauth/tokens", "/../../../oauth/tokens", "/%2e%2e/%2e%2e/oauth/tokens",
		"/banks/../../v2/banks", "/./transactions", "/api/v2/../x/../y",
	} {
		t.Run(p, func(t *testing.T) {
			err := rejectDotSegments(base, p)
			require.Error(t, err)

			_, suggestion := cut(err.Error(), "use ")
			require.NotEmpty(t, suggestion)
			assert.NoError(t, rejectAbsoluteURL(base, suggestion), "suggestion re-rejected as a URL")
			assert.NoError(t, rejectDuplicateBasePath(base, suggestion), "suggestion re-rejected as a repeated prefix")
			assert.NoError(t, rejectDotSegments(base, suggestion), "suggestion still carries a dot segment")
		})
	}
}

func cut(s, sep string) (string, string) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+len(sep):]
}

func TestAPI_PathLeavingTheBaseNeverReachesTheServer(t *testing.T) {
	var reached bool
	f, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	})
	err := run(NewCmdAPI(f), "x/../../../oauth/tokens")
	require.Error(t, err)
	assert.False(t, reached, "the request must not be sent at all")
}

func TestRejectDuplicateBasePath(t *testing.T) {
	const base = "https://cli-api-sandbox.sepay.vn/v2"
	cases := []struct {
		name, base, path string
		wantErr          bool
	}{
		{name: "repeated prefix", base: base, path: "/v2/bank-accounts", wantErr: true},
		{name: "prefix alone", base: base, path: "/v2", wantErr: true},
		{name: "correct relative path", base: base, path: "/bank-accounts"},
		{name: "a path that merely starts with the letters", base: base, path: "/v2beta/x"},
		{name: "root", base: base, path: "/"},
		{name: "base with no path never rejects", base: "http://127.0.0.1:8080", path: "/v2/x"},
		{name: "tail repeated under a longer base", base: "http://127.0.0.1:8079/api/v2", path: "/v2/bank-accounts", wantErr: true},
		{name: "whole longer base repeated", base: "http://127.0.0.1:8079/api/v2", path: "/api/v2/bank-accounts", wantErr: true},
		{name: "tail alone under a longer base", base: "http://127.0.0.1:8079/api/v2", path: "/v2", wantErr: true},
		{name: "the base's other segment is not a candidate", base: "http://127.0.0.1:8079/api/v2", path: "/api-keys"},
		{name: "a longer base leaves an ordinary path alone", base: "http://127.0.0.1:8079/api/v2", path: "/bank-accounts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectDuplicateBasePath(tc.base, tc.path)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), "a mistyped path is a usage error")
		})
	}
}

// net/http fails these locally and the client would retry them as transport errors.
func TestParseHeaders_RejectsWhatTheTransportWouldReject(t *testing.T) {
	cases := []struct {
		name   string
		header string
		ok     bool
	}{
		{"CRLF in the value", "X-Evil: a\r\nX-Inj: 1", false},
		{"bare LF in the value", "X-Evil: a\nb", false},
		{"NUL in the value", "X-Evil: a\x00b", false},
		{"DEL in the value", "X-Evil: a\x7fb", false},
		{"other C0 in the value", "X-Evil: a\x01b", false},
		{"non-ASCII name", "X-Ünicode: ok", false},
		{"space inside the name", "X Space: ok", false},
		{"ordinary header", "X-Trace-Id: abc123", true},
		{"value with commas and quotes", `X-Csv: a,b,"c"`, true},
		{"tab inside the value", "X-Tab: a\tb", true},
		{"non-ASCII value", "X-Note: chuyển khoản", true},
		{"token punctuation in the name", "X-Odd_Name.1: ok", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHeaders([]string{tc.header})
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestParseHeaders_KeepsRepeatedNames(t *testing.T) {
	h, err := parseHeaders([]string{"X-Dup: a", "X-Dup: b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, h.Values("X-Dup"))
}

func TestAPI_PrintsURLsUnescaped(t *testing.T) {
	f, out := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"url":"https://shop.example/cb?a=1&b=<2>"}}`))
	})
	require.NoError(t, run(NewCmdAPI(f), "/webhooks"))
	assert.Contains(t, out.String(), `"url": "https://shop.example/cb?a=1&b=<2>"`)
}

// Only the path names a resource: an id in the query or fragment never reaches it as a segment.
func TestNamedResource(t *testing.T) {
	const xid = "11111111-2222-4333-8444-555555555555"
	cases := []struct {
		path  string
		named bool
	}{
		{"/webhooks/" + xid, true},
		{"/webhooks/" + xid + "/", true},
		{"/webhooks/" + xid + "?x=1", true},
		{"/bank-accounts/" + xid + "/va/" + xid, true},
		{"/bank-accounts/" + xid + "/orders/" + xid + "/va/" + xid, true},
		{"/webhooks/" + strings.ToUpper(xid), true},
		{"/orders/1234", true},
		{"/bank-accounts/" + xid + "/va", false},
		{"/bank-accounts/" + xid + "/orders", false},
		{"/bank-accounts/" + xid + "/orders/" + xid + "/va", false},
		{"/webhook-logs/" + xid + "/replay", false},
		{"/", false},
		{"/webhooks", false},
		{"/webhook-logs/replay", false},
		{"/company/paycode", false},
		{"/webhooks?id=" + xid, false},
		{"/webhooks#/" + xid, false},
		{"/webhooks#" + xid, false},
		{"/webhooks/" + xid[:35], false},
		{"/webhooks/x" + xid, false},
		{"/webhooks/%31%32", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.named, namedResource(tc.path) != "")
		})
	}
}

func TestActionEndpoint_NeverPrintsAQueryValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/webhooks", "/webhooks"},
		{"/webhooks/1", "/webhooks/1"},
		{"/webhooks?access_token=S", "/webhooks?access_token=***"},
		{"/webhooks?a=1&token=S", "/webhooks?a=***&token=***"},
		{"/webhooks?a=1;token=S", "/webhooks?a=***&token=***"},
		{"/webhooks?S", "/webhooks?***"},
		{"/webhooks?access_token%3DS", "/webhooks?***"},
		{"/webhooks?x=1?token=S", "/webhooks?x=***"},
		{"/webhooks?token=a=S", "/webhooks?token=***"},
		{"/webhooks?", "/webhooks?"},
		{"/webhooks#token=S", "/webhooks"},
		{"/webhooks#x?token=S", "/webhooks"},
		{"/webhooks?a=1#S", "/webhooks?a=***"},
		{"/https://u:S@h/v2/webhooks", "/https://***@h/v2/webhooks"},
		{"/https://S@h", "/https://***@h"},
		{"/https://u:S@x@h/", "/https://***@h/"},
		{"//u:S@h/webhooks", "//***@h/webhooks"},
		{"/https://u:S@h/webhooks?t=S", "/https://***@h/webhooks?t=***"},
		{"/https://h/webhooks/a@b", "/https://h/webhooks/a@b"},
		{"/webhooks/a@b", "/webhooks/a@b"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := actionEndpoint(tc.in)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "S")
		})
	}
}
