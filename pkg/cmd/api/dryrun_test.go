package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type apiDryRun struct {
	DryRun  bool `json:"dry_run" yaml:"dry_run"`
	Request struct {
		Method  string   `json:"method" yaml:"method"`
		Path    string   `json:"path" yaml:"path"`
		Query   string   `json:"query" yaml:"query"`
		Headers []string `json:"headers" yaml:"headers"`
		Body    any      `json:"body" yaml:"body"`
	} `json:"request" yaml:"request"`
}

// runAPIDryRun runs `sepay api` with --dry-run against a server that counts every request.
func runAPIDryRun(t *testing.T, global cmdutil.GlobalOptions, stdin string, args ...string) (string, int32, error) {
	t.Helper()
	var requests atomic.Int32
	f, out := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	if global.Output == "" {
		global.Output = "table"
	}
	*f.Global = global
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	if stdin != "" {
		f.IOStreams.In = strings.NewReader(stdin)
	}
	err := run(NewCmdAPI(f), append(args, "--dry-run")...)
	return out.String(), requests.Load(), err
}

// Not even a read is sent: the dry run of `sepay api` is the request itself.
func TestAPIDryRun_SendsNoRequestAnyMethod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		global cmdutil.GlobalOptions
		args   []string
	}{
		{"GET", cmdutil.GlobalOptions{}, []string{"/webhooks"}},
		{"HEAD", cmdutil.GlobalOptions{}, []string{"/webhooks", "-X", "HEAD"}},
		{"sandbox DELETE without --yes or a terminal", cmdutil.GlobalOptions{}, []string{"/webhooks/11111111-2222-4333-8444-555555555555", "-X", "DELETE"}},
		{"production POST without an id, without --yes", cmdutil.GlobalOptions{Env: config.EnvProduction}, []string{"/webhooks", "-X", "POST", "-f", "name=x"}},
		{"production PUT with --yes", cmdutil.GlobalOptions{Env: config.EnvProduction, Yes: true}, []string{"/webhooks/1", "-X", "put"}},
		{"production DELETE, -o yaml", cmdutil.GlobalOptions{Env: config.EnvProduction, Output: "yaml"}, []string{"/webhooks/1", "-X", "DELETE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, requests, err := runAPIDryRun(t, tc.global, "", tc.args...)
			require.NoError(t, err)
			assert.Zero(t, requests)
			assert.Contains(t, out, "dry_run")
		})
	}
}

func TestAPIDryRun_RequestShape(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, "",
		"webhooks/1?page=2", "-X", "post", "-f", "name=x", "-f", "n=42", "-f", "ok=true", "-H", "x-custom: 1")
	require.NoError(t, err)
	var got apiDryRun
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.True(t, got.DryRun)
	assert.Equal(t, "POST", got.Request.Method)
	assert.Equal(t, "/webhooks/1", got.Request.Path, "a leading / added, the query split off")
	assert.Equal(t, "page=2", got.Request.Query)
	assert.Equal(t, []string{"X-Custom"}, got.Request.Headers)
	assert.Equal(t, map[string]any{"name": "x", "n": float64(42), "ok": true}, got.Request.Body, "typed as the real run sends it")
	assert.NotContains(t, out, "Idempotency", "a fresh key per run would never match the real one")
}

func TestAPIDryRun_GETFieldsAreTheQuery(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, "", "/transactions", "-f", "per_page=5", "-f", "per_page=6")
	require.NoError(t, err)
	var got apiDryRun
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "per_page=5&per_page=6", got.Request.Query)
	assert.Nil(t, got.Request.Body)
}

// No credential reaches the output, whether it rides a field, a nested --input key, a header
// value or the query.
func TestAPIDryRun_NeverPrintsACredential(t *testing.T) {
	const secret = "TOPSECRET-1"
	dir := t.TempDir()
	input := filepath.Join(dir, "body.json")
	require.NoError(t, os.WriteFile(input, []byte(`{"name":"x","oauth":{"client_secret":"`+secret+`","access_token":"`+secret+`"},"list":[{"password":"`+secret+`"}],"oauth2_access_token_url":"https://auth.test/t"}`), 0o600))
	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"fields", "", []string{"/x", "-X", "POST", "-f", "secret=" + secret, "-f", "otp=" + secret, "-f", "api_key=" + secret, "-f", "refresh_token=" + secret, "-f", "Password=" + secret}},
		{"camelCase and hyphenated fields", "", []string{
			"/x", "-X", "POST", "-f", "accessToken=" + secret, "-f", "refreshToken=" + secret, "-f", "api-key=" + secret,
			"-f", "subscriber_key=" + secret, "-f", "pin=" + secret, "-f", "passcode=" + secret, "-f", "totp_code=" + secret, "-f", "clientSecret=" + secret,
		}},
		{"headers", "", []string{"/x", "-X", "POST", "-H", "Authorization: Bearer " + secret, "-H", "X-Secret-Key: " + secret}},
		{"--input file", "", []string{"/x", "-X", "PUT", "--input", input}},
		{"--input stdin", `{"secret":"` + secret + `"}`, []string{"/x", "-X", "PUT", "--input", "-"}},
		{"query", "", []string{"/x?token=" + secret, "-f", "otp_code=" + secret}},
		{"query with an encoded key", "", []string{"/x?access%5Ftoken=" + secret + "&a=1"}},
		{"a field after the path's own query", "", []string{"/x?a=1", "-f", "access_token=" + secret}},
		{"a query parameter after a semicolon", "", []string{"/x?page=2;token=" + secret}},
		{"a credential value holding a semicolon", "", []string{"/x?token=abc;" + secret + "&a=1"}},
		{"one-word token and pwd fields", "", []string{"/x", "-X", "POST", "-f", "accesstoken=" + secret, "-f", "pwd=" + secret}},
	}
	for _, tc := range cases {
		for _, output := range []string{"table", "json", "yaml"} {
			t.Run(tc.name+"/"+output, func(t *testing.T) {
				out, requests, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: output}, tc.stdin, tc.args...)
				require.NoError(t, err)
				assert.Zero(t, requests)
				assert.NotContains(t, out, secret)
			})
		}
	}

	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, "", "/x", "-X", "PUT", "--input", input)
	require.NoError(t, err)
	assert.Contains(t, out, `"oauth2_access_token_url": "https://auth.test/t"`, "a URL to a token is not a token")
	assert.Contains(t, out, `"name": "x"`)
}

func TestAPIDryRun_YAMLKeepsIntegers(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "yaml"}, `{"amount":10000000000,"rate":1.5}`, "/x", "-X", "POST", "--input", "-")
	require.NoError(t, err)
	var got apiDryRun
	require.NoError(t, yaml.Unmarshal([]byte(out), &got))
	assert.Contains(t, out, "amount: 10000000000")
	assert.Contains(t, out, "rate: 1.5")
}

// JSON carries a number as the digits sent, however large or precise.
func TestAPIDryRun_JSONKeepsTheDigitsSent(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, `{"big":12345678901234567890,"rate":0.12345678901234567890}`, "/x", "-X", "POST", "--input", "-")
	require.NoError(t, err)
	assert.Contains(t, out, `"big": 12345678901234567890`)
	assert.Contains(t, out, `"rate": 0.12345678901234567890`)
}

// A production write the real run refuses still previews, and the preview names the refusal.
func TestAPIDryRun_NamesTheProductionRefusal(t *testing.T) {
	out, requests, err := runAPIDryRun(t, cmdutil.GlobalOptions{Env: config.EnvProduction, Yes: true, Output: "json"}, "", "/webhooks", "-X", "POST", "-f", "name=x")
	require.NoError(t, err)
	assert.Zero(t, requests)
	var got struct {
		WouldRefuse []string `json:"would_refuse"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.WouldRefuse, 1)
	assert.Contains(t, got.WouldRefuse[0], "names no single resource, so --yes cannot confirm it on production")
}

// The refusal a dry run records names the endpoint, so its query is masked like request.query, on
// both streams; the real run's refusal is the same text.
func TestAPIDryRun_RefusalNeverPrintsACredential(t *testing.T) {
	const secret = "TOPSECRET-1"
	cases := []struct {
		name   string
		global cmdutil.GlobalOptions
		args   []string
		want   string
	}{
		{
			"a write refused on production",
			cmdutil.GlobalOptions{Env: config.EnvProduction},
			[]string{"/webhooks?access_token=" + secret + "#frag", "-X", "POST", "-f", "name=x"},
			"api POST /webhooks?access_token=*** on production needs confirmation",
		},
		{
			"a delete unconfirmed on sandbox",
			cmdutil.GlobalOptions{},
			[]string{"/webhooks/1?token=" + secret, "-X", "DELETE"},
			"api DELETE /webhooks/1?token=***? requires confirmation; pass --yes",
		},
	}
	for _, tc := range cases {
		for _, output := range []string{"table", "json"} {
			t.Run(tc.name+"/"+output, func(t *testing.T) {
				f, out := testFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("a request was sent") })
				errOut := &strings.Builder{}
				f.IOStreams.ErrOut = errOut
				global := tc.global
				global.Output = output
				*f.Global = global
				require.NoError(t, run(NewCmdAPI(f), append(tc.args, "--dry-run")...))
				assert.NotContains(t, out.String()+errOut.String(), secret)
				assert.Contains(t, out.String()+errOut.String(), tc.want)
				if output == "table" {
					assert.Contains(t, errOut.String(), "! the real run would refuse: "+tc.want, "the refusal line of table mode")
				}

				f, _ = testFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("a request was sent") })
				*f.Global = global
				err := run(NewCmdAPI(f), tc.args...)
				require.Error(t, err)
				assert.NotContains(t, err.Error(), secret, "the real run refuses with the same masked text")
			})
		}
	}
}

func TestAPIDryRun_HelpSaysAGETIsNotSentEither(t *testing.T) {
	f, _ := testFactory(t, func(http.ResponseWriter, *http.Request) {})
	assert.Equal(t, "Print the request without sending it, a GET included", NewCmdAPI(f).Flags().Lookup("dry-run").Usage)
}

func TestAPIDryRun_TableModeNoticeOnStderr(t *testing.T) {
	f, out := testFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("a request was sent") })
	errOut := &strings.Builder{}
	f.IOStreams.ErrOut = errOut
	require.NoError(t, run(NewCmdAPI(f), "/x", "-X", "POST", "--dry-run"))
	assert.Equal(t, cmdutil.DryRunNotice+"\n", errOut.String())
	var got apiDryRun
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "sepay api prints JSON in table mode too")
}

func TestCredentialKey(t *testing.T) {
	for key, want := range map[string]bool{
		"secret": true, "oauth2_client_secret": true, "Password": true, "otp": true, "otp_code": true,
		"api_key": true, "apiKey": true, "access_token": true, "token": true, "authorization": true, "credentials": true,
		"accessToken": true, "refreshToken": true, "api-key": true, "subscriber_key": true, "pin": true, "passcode": true,
		"totp_code": true, "clientSecret": true, "X-Api-Key": true, "listen_key": true, "accesstoken": true, "pwd": true,
		"oauth2_access_token_url": false, "name": false, "footprint": false, "token_type": false, "url": false, "key": true,
		"keyword": false, "pinned": false, "auth_type": false, "spin": false, "tokenizer": false,
		"pass": true, "db_pass": true, "jwt": true, "bearer": true, "signature": true, "cvv": true,
		"mat_khau": true, "matKhau": true, "matkhau": true, "refresh": true, "refresh_token": true,
		"passenger": false, "compass": false, "bypass_cache": false, "keys": false, "monkey": false,
		"privateKey": true, "subscriberKey": true, "otpCode": true, "pinCode": true, "keyName": false,
	} {
		assert.Equal(t, want, credentialKey(key), key)
	}
}

// The previewed target is the one the real run sends, quirks included.
func TestAPIDryRun_TargetIsWhatTheClientSends(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantPath  string
		wantQuery string
	}{
		{"a query segment ParseQuery rejects is kept", []string{"/x?a=1;b=2"}, "/x", "a=1;b=2"},
		{"inline query and -f on GET are joined as the client joins them", []string{"/x?a=1", "-f", "b=2"}, "/x", "a=1?b=2"},
		{"no query", []string{"/x"}, "/x", ""},
		{"a fragment never leaves the client", []string{"/x?a=1#frag"}, "/x", "a=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, requests, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, "", tc.args...)
			require.NoError(t, err)
			assert.Zero(t, requests)
			var got apiDryRun
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			assert.Equal(t, tc.wantPath, got.Request.Path)
			assert.Equal(t, tc.wantQuery, got.Request.Query)
		})
	}

	_, requests, err := runAPIDryRun(t, cmdutil.GlobalOptions{}, "", "/webhooks/%zz", "-X", "DELETE")
	require.Error(t, err, "a path the real run cannot build fails the dry run too")
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Zero(t, requests)
}

// An empty or null credential clears one, so it prints as sent, not as "***".
func TestAPIDryRun_AnEmptyCredentialStaysEmpty(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, "", "/x?token=&a=1", "-X", "PUT", "-f", "api_key=", "-f", "secret=null", "-f", "password=p")
	require.NoError(t, err)
	var got apiDryRun
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "token=&a=1", got.Request.Query)
	assert.Equal(t, map[string]any{"api_key": "", "secret": nil, "password": "***"}, got.Request.Body)
}

// The digits of an -f number are those the client encodes, under YAML too.
func TestAPIDryRun_YAMLKeepsTheDigitsOfAField(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "yaml"}, "", "/x", "-X", "POST", "-f", "amount=100000000", "-f", "rate=1.5")
	require.NoError(t, err)
	assert.Contains(t, out, "amount: 100000000\n")
	assert.Contains(t, out, "rate: 1.5\n")
}

// YAML prints every number of an --input body with the digits sent, past int64 and float64 too.
func TestAPIDryRun_YAMLKeepsTheDigitsSent(t *testing.T) {
	const body = `{"big":123456789012345678901234567890,"huge":1e400,"exact":0.1000000000000000055511151231257827,"one":1.0,"neg":-7}`
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "yaml"}, body, "/x", "-X", "POST", "--input", "-")
	require.NoError(t, err)
	for _, line := range []string{"big: 123456789012345678901234567890", "huge: 1e400", "exact: 0.1000000000000000055511151231257827", "one: 1.0", "neg: -7"} {
		assert.Contains(t, out, line+"\n")
	}
}

// Table mode shows an invisible or control character as a \u escape instead of dropping it.
func TestAPIDryRun_TableEscapesInvisibleCharacters(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{}, "", "/x", "-X", "POST",
		"-f", "name=a\u202eb", "-f", "zw=c\u200bd", "-f", "c1=e\u0085f", "-f", "del=g\x7fh", "-f", "tag=i\U000E0041j")
	require.NoError(t, err)
	for _, want := range []string{`a\u202eb`, `c\u200bd`, `e\u0085f`, `g\u007fh`, `i\udb40\udc41j`} {
		assert.Contains(t, out, want)
	}
	for _, raw := range []string{"\u202e", "\u200b", "\u0085", "\x7f", "\U000E0041"} {
		assert.NotContains(t, out, raw)
	}
	var got apiDryRun
	require.NoError(t, json.Unmarshal([]byte(out), &got), "still JSON")
	assert.Equal(t, "a\u202eb", got.Request.Body.(map[string]any)["name"], "the escape decodes to the value sent")
}

// A header -H sets twice is sent twice, and the dry run names it twice.
func TestAPIDryRun_NamesARepeatedHeaderEachTime(t *testing.T) {
	out, _, err := runAPIDryRun(t, cmdutil.GlobalOptions{Output: "json"}, "", "/x", "-X", "POST",
		"-H", "X-Trace: 1", "-H", "x-trace: 2", "-H", "X-Other: 3")
	require.NoError(t, err)
	var got apiDryRun
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, []string{"X-Other", "X-Trace", "X-Trace"}, got.Request.Headers)
}
