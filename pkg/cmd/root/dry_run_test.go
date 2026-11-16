package root

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// dryRunReads answers every read a dry run of prodWrites makes, keyed by path.
var dryRunReads = map[string]string{
	"/webhooks/" + goodXID:      `{"status":"success","data":{"id":"` + goodXID + `","name":"Shop","event_type":"All","authen_type":"HMAC_SHA256","webhook_url":"https://x.test/hook","delivery_mode":"http","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":"2026-07-15 10:00:00","secret_key":"********","request_content_type":"Json"}}`,
	"/bank-accounts/" + goodXID: `{"status":"success","data":{"id":"` + goodXID + `","account_holder_name":"NGUYEN VAN A","account_number":"0000000009","accumulated":0,"last_transaction":null,"label":"Shop","account_type":"individual","active":"1","created_at":"2026-07-15 10:00:00","bank_short_name":"ACB","bank_full_name":"ACB","bank_bin":"970416","bank_code":"ACB"}}`,
	"/transactions/tx-1":        `{"status":"success","data":{"id":"tx-1","transfer_type":"in","amount_in":2500000,"amount_out":0,"bank_brand_name":"ACB","bank_account_id":"ba_1"}}`,
	"/webhook-logs":             `{"status":"success","data":[{"transaction_id":"tx-1","webhook_xid":"` + goodXID + `","replayable":true,"status":"failed","request_url":"https://x.test/hook","connect_success":true,"response_status_code":500,"created_at":"2026-07-15 10:00:00"}],"meta":{"pagination":{"total":1,"per_page":1,"current_page":1,"last_page":1,"has_more":false}}}`,
	"/company/paycode":          currentPaycode,
}

type dryRunRun struct {
	writes, reads int32
	out, errOut   string
	err           error
}

// runDryRun runs args against a server that answers dryRunReads (or 404 when missing is set) and
// counts every request that is not a GET.
func runDryRun(t *testing.T, defaultEnv string, args []string, missing bool, setup ...func(*cmdutil.Factory)) dryRunRun {
	t.Helper()
	var writes, reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		reads.Add(1)
		body, ok := dryRunReads[r.URL.Path]
		if !ok || missing {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"not_found","message":"not found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())

	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
	f.Config = func() (*config.Config, error) {
		cfg, err := config.Load()
		if err == nil && defaultEnv != "" {
			cfg.Set("default_env", defaultEnv)
		}
		return cfg, err
	}
	// The real closure sets ReadOnly from --dry-run (TestNewAPIClient_DryRunBuildsAReadOnlyClient);
	// leaving it off here proves each command stops on its own before the write.
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	for _, fn := range setup {
		fn(f)
	}
	cmd := NewCmdRoot(f)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return dryRunRun{writes: writes.Load(), reads: reads.Load(), out: out.String(), errOut: errOut.String(), err: err}
}

// previewedWrites are the prodWrites cases a dry run previews. A blank target is left out: its dry
// run stops at the flag check after the read, and the case belongs to the production-write rule's tests.
func previewedWrites() []prodWriteCase {
	var out []prodWriteCase
	for _, w := range prodWrites {
		if !slices.ContainsFunc(w.args, func(a string) bool { return a != "" && strings.TrimSpace(a) == "" }) {
			out = append(out, w)
		}
	}
	return out
}

// writeMethod is the method of the write each prodWrites case withholds.
func writeMethod(args []string) string {
	for i, a := range args {
		if a == "-X" {
			return args[i+1]
		}
	}
	switch strings.Join(args[:2], " ") {
	case "webhook create", "webhook replay", "paycode create":
		return http.MethodPost
	case "webhook delete":
		return http.MethodDelete
	}
	return http.MethodPut
}

// Every production write, under every flag combination that changes how it confirms or prints:
// nothing is sent, the exit is 0, and the output is the dry run's.
func TestDryRun_EveryWriteSendsNothing(t *testing.T) {
	variants := []struct {
		name       string
		defaultEnv string
		flags      []string
		output     string
	}{
		{"production --env, no --yes, no terminal, -o json", "", []string{"--env", "production", "-o", "json"}, "json"},
		{"production --env with --yes", "", []string{"--env", "production", "--yes"}, "table"},
		{"default_env production without --env, -o yaml", config.EnvProduction, []string{"-o", "yaml"}, "yaml"},
		{"default_env production with --yes", config.EnvProduction, []string{"--yes", "--json"}, "json"},
		{"production --api-base host", "", []string{"--api-base", "https://cli-api.sepay.vn/v2"}, "table"},
		{"sandbox, no --yes, --quiet", "", []string{"--quiet"}, "table"},
		{"sandbox --no-input", "", []string{"--no-input"}, "table"},
		{"sandbox with a valid --idempotency-key", "", []string{"--idempotency-key", "dry-run-key-1"}, "table"},
	}
	for _, w := range previewedWrites() {
		for _, v := range variants {
			args := append(append(append([]string{}, w.args...), "--dry-run"), v.flags...)
			t.Run(strings.Join(w.args, " ")+"/"+v.name, func(t *testing.T) {
				r := runDryRun(t, v.defaultEnv, args, false)
				require.NoError(t, r.err)
				assert.Zero(t, r.writes)

				switch v.output {
				case "json":
					var got map[string]any
					require.NoError(t, json.Unmarshal([]byte(r.out), &got), "stdout is one JSON object: %q", r.out)
					assert.Equal(t, true, got["dry_run"])
					req, _ := got["request"].(map[string]any)
					assert.Equal(t, writeMethod(w.args), req["method"])
				case "yaml":
					var got map[string]any
					require.NoError(t, yaml.Unmarshal([]byte(r.out), &got))
					assert.Equal(t, true, got["dry_run"])
				default:
					assert.Contains(t, r.errOut, cmdutil.DryRunNotice)
					assert.NotEmpty(t, r.out, "the preview is on stdout")
				}
				if v.output != "table" {
					for _, s := range []string{"Dry run", "! not checked", "! live", "env:"} {
						assert.NotContains(t, r.errOut, s, "machine output keeps stderr to warnings the real run has")
					}
				}
				assert.NotContains(t, r.errOut, "! live", "a dry run is not a live write")
			})
		}
	}
}

// Each production write the rule refuses still previews, and would_refuse names the refusal the real
// run stops at.
func TestDryRun_NamesTheProductionRefusal(t *testing.T) {
	for _, w := range previewedWrites() {
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			r := runDryRun(t, config.EnvProduction, append(append([]string{}, w.args...), "--dry-run", "--json"), false)
			require.NoError(t, r.err)
			assert.Zero(t, r.writes)
			var got struct {
				WouldRefuse []string `json:"would_refuse"`
			}
			require.NoError(t, json.Unmarshal([]byte(r.out), &got), r.out)
			require.NotEmpty(t, got.WouldRefuse)
			assert.Contains(t, got.WouldRefuse[0], "without --env production on the command line (default_env does not count)")
		})
	}
}

// On a terminal a dry run asks for no confirmation; prompttest fails on any unscripted prompt.
func TestDryRun_TerminalAsksNothing(t *testing.T) {
	for _, w := range previewedWrites() {
		for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
			t.Run(strings.Join(w.args, " ")+"/"+env, func(t *testing.T) {
				args := append(append([]string{}, w.args...), "--dry-run", "--env", env)
				if w.path == "sepay webhook create" {
					// Left out, these are input prompts the real run asks too, not confirmations.
					args = append(args, "--name", "x", "--auth-type", "none")
				}
				r := runDryRun(t, "", args, false, func(f *cmdutil.Factory) { prompttest.Use(t, f) })
				require.NoError(t, r.err)
				assert.Zero(t, r.writes)
			})
		}
	}
}

// A read the dry run needs fails as that read would: the exit is not 0 and nothing is written.
func TestDryRun_FailedReadExitsAsTheRead(t *testing.T) {
	for _, w := range previewedWrites() {
		if !w.named {
			continue
		}
		if w.path == "sepay api" {
			continue // it reads nothing
		}
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			r := runDryRun(t, "", append(append([]string{}, w.args...), "--dry-run", "--env", "production", "--json"), true)
			require.Error(t, r.err)
			assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(r.err), r.err.Error())
			assert.Zero(t, r.writes)
			assert.Empty(t, r.out, "no dry-run object after a failed read")
		})
	}
}

// --dry-run=false is the real run, so the production rule applies again.
func TestDryRun_FalseIsTheRealRun(t *testing.T) {
	for _, w := range previewedWrites() {
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			r := runDryRun(t, config.EnvProduction, append(append([]string{}, w.args...), "--dry-run=false", "--yes"), false)
			require.Error(t, r.err)
			assert.Contains(t, r.err.Error(), "default_env does not count")
			assert.Zero(t, r.writes)
		})
	}
}

// A flag check the command makes before its write still refuses under --dry-run.
func TestDryRun_FlagChecksStillRun(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"paycode", "update", "--enable", "--disable"}, "cannot use --enable and --disable together"},
		{[]string{"paycode", "update", "--structure", "DH", "--suffix-to", "31"}, "--suffix-to must be between 1 and 30"},
		{[]string{"paycode", "update", "--structure", "XX", "--suffix-to", "9"}, `no pay code structure with prefix "XX"`},
		{[]string{"paycode", "update", "--structure", "DH", "--suffix-from", "11"}, "suffix length from 11 is greater than to 10"},
		{[]string{"paycode", "create", "D1", "--suffix-from", "3", "--suffix-to", "10", "--character-type", "NumberOnly", "--active"}, "prefix must be 2-5 letters"},
		{[]string{"paycode", "create", "HD", "--suffix-from", "3", "--suffix-to", "10", "--active"}, "--character-type is required"},
		{[]string{"paycode", "update"}, "a pay code prefix is required"},
		{[]string{"bank-account", "update", goodXID}, "--label is required"},
		{[]string{"bank-account", "update", "--label", "x"}, "a bank account xid is required"},
		{[]string{"webhook", "update", goodXID}, "provide at least one field to update"},
		{[]string{"webhook", "update", goodXID, "--event-type", "both"}, "--event-type"},
		{[]string{"webhook", "update", goodXID, "--secret", "s"}, "--secret needs --auth-type"},
		{[]string{"webhook", "create", "--url", "https://example.com/hook", "--bank-mode", "every"}, "--bank-mode"},
		{[]string{"webhook", "create", "--bank-mode", "all"}, "--url"},
		{[]string{"webhook", "create", "--listen-only", "--bank-mode", "all", "--env", "production"}, "--listen-only is sandbox-only"},
		{[]string{"webhook", "delete"}, "a webhook xid is required"},
		{[]string{"webhook", "delete", ".."}, "xid"},
		{[]string{"webhook", "replay"}, "a transaction id is required"},
		{[]string{"webhook", "replay", "tx-1", "--transaction", "tx-2"}, "not both"},
		{[]string{"webhook", "replay", "tx-1", "--webhook", ""}, "--webhook needs a value"},
		{[]string{"api", "-X", "POST", "/webhooks", "-f", "novalue"}, `invalid --field "novalue"`},
		{[]string{"api", "-X", "POST", "/webhooks", "-H", "Bad Header: x"}, "invalid --header name"},
		{[]string{"api", "-X", "POST", "https://my.sepay.vn/v2/webhooks"}, "must not be a full URL"},
		{[]string{"api", "-X", "DELETE", "/webhooks/../oauth/tokens"}, `must not contain "." or ".." segments`},
		{[]string{"api", "/webhooks", "--input", "x.json"}, "--input is not allowed with GET"},
		{[]string{"api", "-X", "POST", "/webhooks", "--input", "/nonexistent/body.json"}, "could not read --input file"},
		{[]string{"webhook", "replay", "tx-1", "--webhook", goodXID, "--idempotency-key", "bad key"}, "--idempotency-key must be"},
		{[]string{"api", "-X", "POST", "/webhooks", "--idempotency-key", "short"}, "--idempotency-key must be"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			r := runDryRun(t, "", append(append([]string{}, tc.args...), "--dry-run"), false)
			require.Error(t, r.err)
			assert.Contains(t, r.err.Error(), tc.want)
			assert.NotEqual(t, 0, cmdutil.ExitCodeForError(r.err))
			assert.Zero(t, r.writes)
			assert.NotContains(t, r.errOut, cmdutil.DryRunNotice, "a refused dry run says nothing was previewed")
		})
	}
}

// --dry-run is a flag of exactly the production writes; anywhere else it is an unknown flag, so an
// agent cannot believe a read-or-sandbox command previewed something.
func TestDryRun_FlagOnExactlyTheProductionWrites(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var withFlag []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() && c.LocalNonPersistentFlags().Lookup("dry-run") != nil {
			withFlag = append(withFlag, c.CommandPath())
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(NewCmdRoot(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}))
	assert.ElementsMatch(t, prodWritePaths(), withFlag)

	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))
	var inMap []string
	for _, c := range m.Commands {
		for _, fl := range c.Flags {
			if fl.Name == "dry-run" {
				assert.Equal(t, "bool", fl.Type, c.Path)
				inMap = append(inMap, c.Path)
			}
		}
	}
	assert.ElementsMatch(t, prodWritePaths(), inMap, "--map=json lists the flag where it exists")

	for _, args := range [][]string{
		{"webhook", "list", "--dry-run"},
		{"bank-account", "create", "--dry-run"},
		{"bank-account", "delete", goodXID, "--dry-run"},
		{"paycode", "list", "--dry-run"},
		{"va", "list", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := runDryRun(t, "", args, false)
			require.Error(t, r.err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(r.err))
			assert.Contains(t, r.err.Error(), "unknown flag: --dry-run")
			assert.Zero(t, r.writes)
		})
	}
}
