package root

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeAnswers is what the fake API answers each write with, enough for the real run to finish.
var writeAnswers = map[string]string{
	"/webhooks":                 dryRunReads["/webhooks/"+goodXID],
	"/webhooks/" + goodXID:      dryRunReads["/webhooks/"+goodXID],
	"/bank-accounts/" + goodXID: `{"status":"success","data":{"id":"` + goodXID + `"}}`,
	"/company/paycode": `{"status":"success","data":{"structure":{"is_active":"on","prefix":"HD","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"},` +
		`"PayCode":"on","PayCodeStructures":[],"version":"v2"}}`,
	"/webhook-logs/replay": `{"status":"success","data":{"delivered":true,"message":"ok","log":null}}`,
}

// sentRequest is a write as the API receives it, or as a dry run prints it.
type sentRequest struct {
	Method string
	Path   string
	Query  string
	Body   any
	// Headers names the headers of checkedHeaders the write carries.
	Headers []string
}

// checkedHeaders are the headers a write carries beside the client's own: paycode update's If-Match
// and the one requestWrites sets with -H.
var checkedHeaders = []string{"If-Match", "X-Trace"}

// requestWrites widen prodWrites with the bodies, credentials, query and headers a write can carry.
var requestWrites = [][]string{
	{
		"webhook", "update", goodXID, "--auth-type", "hmac", "--secret", "hmac-secret", "--content-type", "form", "--event-type", "in",
		"--prefix-filter", "DH,HD", "--verify-payment", "--name", " Shop #1 ",
	},
	{
		"webhook", "update", goodXID, "--auth-type", "oauth2", "--oauth2-client-id", "cid", "--oauth2-client-secret", "oauth2-secret",
		"--oauth2-token-url", "https://auth.example.com/t",
	},
	{
		"webhook", "create", "--url", "https://example.com/hook", "--bank-mode", "all", "--name", "Shop", "--auth-type", "apikey",
		"--secret", "apikey-secret", "--event-type", "out", "--skip-if-no-code",
	},
	{"webhook", "replay", "tx-1", "--webhook", goodXID, "--force"},
	{"paycode", "update", "--structure", "DH", "--prefix", "HD", "--suffix-from", "4", "--character-type", "NumberAndLetter", "--active=false"},
	{"paycode", "create", "HD", "--suffix-from", "4", "--suffix-to", "8", "--character-type", "NumberAndLetter", "--active=false"},
	{"bank-account", "update", goodXID, "--label", "Shop #1 ✓"},
	{"bank-account", "update", goodXID, "--label", " Shop "},
	{"api", "-X", "POST", "/webhooks?trace=1&x=a%20b", "-f", "name=x", "-f", "is_verify_payment=true", "-H", "X-Trace: 1"},
	{"api", "-X", "PUT", "/webhooks/" + goodXID + "?token=", "-f", "secret=", "-f", "amount=100000000"},
	{"api", "-X", "DELETE", "/webhooks/" + goodXID},
}

// maskedSecrets is body with each credential a webhook write sets as "***", as a dry run prints it.
func maskedSecrets(body any) any {
	m, ok := body.(map[string]any)
	if !ok {
		return body
	}
	for _, k := range []string{"secret", "oauth2_client_secret"} {
		if v, _ := m[k].(string); v != "" {
			m[k] = "***"
		}
	}
	return m
}

// runWrite runs args on sandbox with --yes against a fake API that answers reads from dryRunReads
// and records the one write the run sends.
func runWrite(t *testing.T, args []string) (stdout string, write *sentRequest) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			body, ok := dryRunReads[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
			return
		}
		if write != nil {
			t.Errorf("a second write: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		write = &sentRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		for _, name := range checkedHeaders {
			if r.Header.Get(name) != "" {
				write.Headers = append(write.Headers, name)
			}
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &write.Body); err != nil {
				t.Errorf("write body is not JSON: %v", err)
			}
		}
		answer, ok := writeAnswers[r.URL.Path]
		if !ok {
			answer = `{"status":"success","data":{}}`
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())

	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
	f.Config = config.Load
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	cmd := NewCmdRoot(f)
	cmd.SetArgs(append(args, "--yes"))
	require.NoError(t, cmd.ExecuteContext(context.Background()), args)
	return out.String(), write
}

// The request a dry run prints is the request the real run sends: method, path, query, body and
// headers, for every production write; only each credential reads "***".
func TestDryRun_PrintsTheRequestTheRealRunSends(t *testing.T) {
	var cases [][]string
	for _, w := range previewedWrites() {
		cases = append(cases, w.args)
	}
	for _, args := range append(cases, requestWrites...) {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, withheld := runWrite(t, append(append([]string{}, args...), "--dry-run", "-o", "json"))
			require.Nil(t, withheld, "the dry run sent its write")
			var printed struct {
				Request struct {
					Method  string   `json:"method"`
					Path    string   `json:"path"`
					Query   string   `json:"query"`
					Headers []string `json:"headers"`
					Body    any      `json:"body"`
				} `json:"request"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &printed), out)

			_, sent := runWrite(t, args)
			require.NotNil(t, sent, "the real run sent no write")
			sent.Body = maskedSecrets(sent.Body)
			assert.Equal(t, sentRequest{
				Method:  printed.Request.Method,
				Path:    printed.Request.Path,
				Query:   printed.Request.Query,
				Body:    printed.Request.Body,
				Headers: printed.Request.Headers,
			}, *sent)
		})
	}
}
