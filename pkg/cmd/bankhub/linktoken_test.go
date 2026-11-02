package bankhub

import (
	"io"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const linkTokenGet = `{"xid":"lt_1","purpose":"LINK_BANK_ACCOUNT","status":"ACTIVE","completion_redirect_uri":"","is_mobile_app":0,"language":"vi","session_count":"0","created_at":"2026-07-23 10:00:00","updated_at":"2026-07-23 10:00:00","expires_at":"2026-07-24 10:00:00","company":{"xid":"co_1","full_name":"E2E Test Company","status":"Active"},"sessions":[],"events":[]}`

func TestLinkTokenGet_FlatShape(t *testing.T) {
	var gotBody string
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(linkTokenGet))
	})
	require.NoError(t, run(f, "link-token", "get", "tok_abc"))
	s := out.String()
	assert.Contains(t, s, "lt_1")
	assert.Contains(t, s, "LINK_BANK_ACCOUNT")
	assert.Contains(t, s, "ACTIVE")
	assert.Contains(t, s, "E2E Test Company")
	// get takes the token value, not the xid.
	assert.Contains(t, gotBody, `"link_token":"tok_abc"`)
}

func TestLinkTokenGet_JSONFlat(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(linkTokenGet))
	f.Global.Output = "json"
	require.NoError(t, run(f, "link-token", "get", "tok_abc"))
	assert.Contains(t, out.String(), `"xid": "lt_1"`)
	assert.Contains(t, out.String(), `"company"`)
}

// is_mobile_app may be a JSON bool.
func TestLinkTokenGet_ToleratesBoolIsMobileApp(t *testing.T) {
	body := `{"xid":"lt_2","purpose":"LINK_BANK_ACCOUNT","status":"ACTIVE","is_mobile_app":true,"language":"vi","session_count":"0","company":{"xid":"co_1","full_name":"X","status":"Active"},"sessions":[],"events":[]}`
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(body))
	require.NoError(t, run(f, "link-token", "get", "tok"))
	assert.Contains(t, out.String(), "lt_2")
}

func TestLinkTokenGet_NotFoundStringCode(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"RESOURCE_NOT_FOUND","message":"Không tìm thấy tài nguyên"}`))
	})
	err := run(f, "link-token", "get", "nope")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	var he *api.HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "RESOURCE_NOT_FOUND", he.Code) // string-code error shape (c)
}

func TestLinkTokenCreate_PostsAndShowsURL(t *testing.T) {
	var body []byte
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"xid":"lt_new","hosted_link_url":"https://link.example/abc","link_token":"tok_abc","expires_at":"2026-07-24 10:00:00"}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "link-token", "create", "--company", "co_1", "--purpose", "LINK_BANK_ACCOUNT"))
	assert.Contains(t, string(body), `"company_xid":"co_1"`)
	assert.Contains(t, string(body), `"purpose":"LINK_BANK_ACCOUNT"`)
	assert.Contains(t, out.String(), "lt_new")
	assert.Contains(t, out.String(), "https://link.example/abc")
}

func TestLinkTokenCreate_MasksTokenInJSON(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(
		`{"xid":"lt_new","hosted_link_url":"https://link.example/abc","link_token":"tok_abc","expires_at":"2026-07-24 10:00:00"}`))
	f.Global.Yes = true
	f.Global.Output = "json"
	require.NoError(t, run(f, "link-token", "create", "--company", "co_1", "--purpose", "LINK_BANK_ACCOUNT"))
	assert.NotContains(t, out.String(), "tok_abc")
	assert.Contains(t, out.String(), "****")
}

func TestLinkTokenCreate_ShowTokenRevealsInJSONAndTable(t *testing.T) {
	const created = `{"xid":"lt_new","hosted_link_url":"https://link.example/abc","link_token":"tok_abc","expires_at":"2026-07-24 10:00:00"}`

	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(created))
			f.Global.Yes = true
			f.Global.Output = format

			require.NoError(t, run(f, "link-token", "create", "--company", "co_1",
				"--purpose", "LINK_BANK_ACCOUNT", "--show-token"))

			assert.Contains(t, out.String(), "tok_abc")
			assert.NotContains(t, out.String(), "****")
		})
	}
}

func TestLinkTokenCreate_DefaultTableOmitsToken(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(
		`{"xid":"lt_new","hosted_link_url":"https://link.example/abc","link_token":"tok_abc","expires_at":"2026-07-24 10:00:00"}`))
	f.Global.Yes = true

	require.NoError(t, run(f, "link-token", "create", "--company", "co_1", "--purpose", "LINK_BANK_ACCOUNT"))

	assert.NotContains(t, out.String(), "tok_abc")
	assert.NotContains(t, out.String(), "Token:")
}

func TestLinkTokenCreate_NoPurposeNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	f.Global.Yes = true
	err := run(f, "link-token", "create", "--company", "co_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestLinkTokenRevoke_PostsXID(t *testing.T) {
	var body []byte
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"message":"Liên kết đã được thu hồi thành công"}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "link-token", "revoke", "lt_1"))
	assert.Contains(t, string(body), `"link_token_xid":"lt_1"`)
	assert.Contains(t, out.String(), "Link token revoked.")
	assert.NotContains(t, out.String(), "thu hồi")
}

func TestLinkTokenRevoke_NoArgNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	f.Global.Yes = true
	err := run(f, "link-token", "revoke")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestLinkTokenCreate_PurposeAndLanguageValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bogus purpose", []string{"--purpose", "LINK_BANK"}, "LINK_BANK_ACCOUNT, UNLINK_BANK_ACCOUNT"},
		{"wrong-case purpose", []string{"--purpose", "create_va"}, "CREATE_VA"},
		{"bogus language", []string{"--purpose", "CREATE_VA", "--language", "de"}, "--language must be one of: vi, en"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server on invalid input")
			})
			f.Global.Yes = true

			err := run(f, append([]string{"link-token", "create", "--company", "co_1"}, tt.args...)...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLinkTokenCreate_EmptyLanguageIsUnset(t *testing.T) {
	var body []byte
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"xid":"lt_new","hosted_link_url":"https://link.example/abc","link_token":"tok_abc"}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "link-token", "create", "--company", "co_1", "--purpose", "CREATE_VA", "--language", ""))
	assert.NotContains(t, string(body), "language")
}

func TestLinkTokenGet_NoArgNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without a link token")
	})
	err := run(f, "link-token", "get")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}
