package cmdutil

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// The command harnesses stub NewAPIClient, so only this test fails when the line that makes a dry
// run's client read-only is deleted.
func TestNewAPIClient_DryRunBuildsAReadOnlyClient(t *testing.T) {
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	}))
	defer srv.Close()

	for _, dryRun := range []bool{true, false} {
		dir := t.TempDir()
		keyring.MockInit()
		h, err := config.LoadHostsFrom(dir)
		require.NoError(t, err)
		h.Active = "work"
		h.Profiles["work"] = &config.Profile{
			Host: "my.sepay.vn", Insecure: true, OAuthToken: "at", RefreshToken: "rt",
			ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339), Scopes: []string{"sandbox"},
		}
		require.NoError(t, h.Save())
		t.Setenv("SEPAY_CONFIG_DIR", dir)
		t.Setenv("SEPAY_SANDBOX_API_URL", srv.URL+"/v2")

		f := New()
		ios, _, _, _ := iostreams.Test()
		f.IOStreams = ios
		f.Global.DryRun = dryRun
		client, err := f.NewAPIClient()
		require.NoError(t, err)
		assert.Equal(t, dryRun, client.ReadOnly)

		_, err = client.Get(context.Background(), "/webhooks", nil)
		require.NoError(t, err, "a dry run still reads")
		_, err = client.Post(context.Background(), "/webhooks", api.WriteOptions{IdempotencyKey: "key-12345678"}) // gitleaks:allow
		if dryRun {
			require.ErrorIs(t, err, api.ErrReadOnly)
		} else {
			require.NoError(t, err)
		}
	}
	assert.Equal(t, int32(1), writes.Load(), "only the run without --dry-run wrote")
}

// A dry run passes every confirm gate without prompting, and keeps the refusal the real run would stop at.
func TestConfirmGates_DryRunNeitherRefusesNorPrompts(t *testing.T) {
	const typed = "forced replay must be confirmed on a terminal; --yes does not confirm it"
	cases := []struct {
		name       string
		global     GlobalOptions
		defaultEnv string
		// what would_refuse holds without and with a terminal
		noTerminal, terminal []string
	}{
		{
			"production without --yes",
			GlobalOptions{Env: config.EnvProduction, DryRun: true},
			"",
			[]string{"create webhook on production needs confirmation; ask the user"},
			nil,
		},
		{
			"production with --yes and no named resource",
			GlobalOptions{Env: config.EnvProduction, Yes: true, DryRun: true},
			"",
			[]string{"names no single resource"},
			[]string{"names no single resource"},
		},
		{
			"default_env production without --env",
			GlobalOptions{DryRun: true},
			config.EnvProduction,
			[]string{"without --env production on the command line"},
			[]string{"add --env production to the command"},
		},
		{
			"production host from --api-base",
			GlobalOptions{APIBase: "https://cli-api.sepay.vn/v2", DryRun: true},
			"",
			[]string{"which is not the sandbox host"},
			[]string{"which is not the sandbox host"},
		},
		{
			"sandbox without --yes",
			GlobalOptions{Env: config.EnvSandbox, DryRun: true},
			"",
			[]string{"delete webhook x? requires confirmation; pass --yes"},
			nil,
		},
		{
			"sandbox with --yes",
			GlobalOptions{Env: config.EnvSandbox, Yes: true, DryRun: true},
			"",
			[]string{typed},
			nil,
		},
		{
			"production with -o json",
			GlobalOptions{Env: config.EnvProduction, Output: "json", DryRun: true},
			"",
			[]string{"needs confirmation"},
			[]string{"needs confirmation"},
		},
	}
	for _, tc := range cases {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/terminal=%v", tc.name, terminal), func(t *testing.T) {
				want := tc.noTerminal
				if terminal {
					want = tc.terminal
				}
				refused := runConfirmGates(t, tc.global, tc.defaultEnv, terminal)
				require.Len(t, refused, len(want), "%q", refused)
				for i, w := range want {
					assert.Contains(t, refused[i], w)
				}
			})
		}
	}
}

func runConfirmGates(t *testing.T, global GlobalOptions, defaultEnv string, terminal bool) []string {
	t.Helper()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, out, errOut := iostreams.Test()
	ios.SetStdinTTY(terminal)
	ios.SetStdoutTTY(terminal)
	ios.SetStderrTTY(terminal)
	f := &Factory{IOStreams: ios, Global: &global, Prompter: failingPrompter{t}}
	f.Config = func() (*config.Config, error) {
		cfg, err := config.Load()
		if err == nil && defaultEnv != "" {
			cfg.Set("default_env", defaultEnv)
		}
		return cfg, err
	}

	require.NoError(t, f.CheckProdWrite("create webhook", ""))
	require.NoError(t, f.ConfirmProdWrite("create webhook", ""))
	require.NoError(t, f.ConfirmMutation("delete webhook x", "", true))
	require.NoError(t, f.ConfirmByTyping("forced replay", "tx-1"))
	assert.Empty(t, out.String())
	assert.NotContains(t, errOut.String(), "! live")
	return f.wouldRefuse()
}

// Without --dry-run the same gates still refuse, so the skip above is the flag's doing.
func TestConfirmGates_RefuseWithoutDryRun(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, _, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: config.EnvProduction}}
	require.Error(t, f.CheckProdWrite("create webhook", ""))
	require.Error(t, f.ConfirmByTyping("forced replay", "tx-1"))
}

type failingPrompter struct{ t *testing.T }

func (p failingPrompter) fail(label string) { p.t.Fatalf("a dry run prompted: %q", label) }
func (p failingPrompter) Select(label string, _ []string, _ int) (int, error) {
	p.fail(label)
	return 0, nil
}

func (p failingPrompter) MultiSelect(label string, _ []string, _ []int) ([]int, error) {
	p.fail(label)
	return nil, nil
}
func (p failingPrompter) Input(label, _ string) (string, error) { p.fail(label); return "", nil }
func (p failingPrompter) Password(label string) (string, error) { p.fail(label); return "", nil }
func (p failingPrompter) Confirm(label string, _ bool) (bool, error) {
	p.fail(label)
	return false, nil
}

func (p failingPrompter) Accounts(label string, _ []FormAccount) ([]FormAnswer, error) {
	p.fail(label)
	return nil, nil
}

func TestRenderDryRun_MachineOutputIsOneObjectAndNothingElse(t *testing.T) {
	for _, output := range []string{"json", "yaml"} {
		t.Run(output, func(t *testing.T) {
			ios, _, out, errOut := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: output}}
			d := DryRun{Request: DryRunRequest{Method: "PUT", Path: "/x"}}
			err := f.RenderDryRun(d, func() error {
				t.Fatal("the table renders in table mode only")
				return nil
			})
			require.NoError(t, err)
			assert.Empty(t, errOut.String(), "no notice and no not-checked line beside the object")
			assert.Contains(t, out.String(), "dry_run")
			if output == "json" {
				var got map[string]any
				require.NoError(t, json.Unmarshal(out.Bytes(), &got))
				assert.Equal(t, []string{"after", "before", "dry_run", "not_checked", "request", "would_refuse"}, slices.Sorted(maps.Keys(got)))
				assert.Equal(t, true, got["dry_run"])
				assert.Nil(t, got["before"])
				assert.Nil(t, got["after"])
				assert.Equal(t, []any{DryRunNotChecked}, got["not_checked"])
				assert.Equal(t, []any{}, got["would_refuse"])
			}
		})
	}
}

// The keys come in the contract's order, which a reader of the raw output relies on.
func TestRenderDryRun_KeysInTheContractOrder(t *testing.T) {
	keys := []string{"dry_run", "request", "method", "path", "query", "headers", "body", "before", "after", "not_checked", "would_refuse"}
	for _, output := range []string{"json", "yaml"} {
		t.Run(output, func(t *testing.T) {
			ios, _, out, _ := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: output}}
			d := DryRun{
				Request: DryRunRequest{Method: "PUT", Path: "/x", Query: "a=1", Headers: []string{"If-Match"}, Body: map[string]any{"k": "v"}},
				Before:  map[string]any{"k": "u"},
				After:   map[string]any{"k": "v"},
			}
			require.NoError(t, f.RenderDryRun(d, func() error { return nil }))
			text, last := "\n"+out.String(), -1
			for _, key := range keys {
				at := regexp.MustCompile(`\n\s*"?` + key + `"?:`).FindStringIndex(text)
				require.NotNil(t, at, key)
				assert.Greater(t, at[0], last, key)
				last = at[0]
			}
		})
	}
}

func TestRenderDryRun_TableKeepsTheNoticeUnderQuiet(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		ios, _, out, errOut := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Quiet: quiet}}
		err := f.RenderDryRun(DryRun{}, func() error { f.Printf("TABLE\n"); return nil })
		require.NoError(t, err)
		assert.Equal(t, "TABLE\n", out.String())
		assert.Equal(t, DryRunNotice+"\n! not checked: "+DryRunNotChecked+"\n", errOut.String())
	}
}

// would_refuse holds the CLI's own gate the real run stops at, and nothing of the server's.
func TestRenderDryRun_ReportsWhatTheRealRunWouldRefuse(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	for _, output := range []string{"", "json"} {
		ios, _, out, errOut := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: config.EnvProduction, Output: output, DryRun: true}}
		f.Config = config.Load
		require.NoError(t, f.ConfirmProdWrite("update webhook", "wh-1"))
		err := f.RenderDryRun(DryRun{}, func() error { return nil })
		require.NoError(t, err)
		gate := "update webhook on production needs confirmation; ask the user to run this command in their own terminal"
		if output == "json" {
			var got DryRun
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			assert.Equal(t, []string{gate}, got.WouldRefuse)
			continue
		}
		assert.Equal(t, DryRunNotice+"\n! not checked: "+DryRunNotChecked+"\n! the real run would refuse: "+gate+"\n", errOut.String())
	}
}

// The request of a `sepay api` dry run is echoed text: table mode strips controls, -o json keeps every byte.
func TestRenderDryRunRequest_StripsTerminalControlsInTableMode(t *testing.T) {
	poisoned := "https://a\u202egnp.evil\u009b2J" + escapePayload
	req := DryRunRequest{Method: "POST", Path: "/webhooks", Body: map[string]any{"url": poisoned}}
	for _, output := range []string{"", "json"} {
		ios, _, out, _ := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: output}}
		require.NoError(t, f.RenderDryRunRequest(req))
		if output == "json" {
			var got struct {
				Request struct {
					Body map[string]string `json:"body"`
				} `json:"request"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			assert.Equal(t, poisoned, got.Request.Body["url"])
			continue
		}
		for _, bad := range []string{"\u202e", "\u009b", "\x1b", "\x07"} {
			assert.NotContains(t, out.String(), bad)
		}
		assert.Contains(t, out.String(), "\n  \"request\": {\n", "the JSON keeps its lines")
	}
}

func TestRenderChanges(t *testing.T) {
	type cfg struct {
		On   bool   `json:"on"`
		Mode string `json:"mode"`
	}
	type res struct {
		Name    string         `json:"name"`
		URL     string         `json:"url"`
		Key     string         `json:"key,omitempty"`
		Tags    []string       `json:"tags"`
		Config  map[string]cfg `json:"config,omitempty"`
		List    []cfg          `json:"list"`
		Pointer *string        `json:"pointer"`
	}
	x := "x"
	before := res{
		Name: "a", URL: "https://a", Key: "***", Tags: []string{"A", "B"},
		Config: map[string]cfg{"ba1": {true, "all"}, "ba2": {false, "list"}},
		List:   []cfg{{true, "all"}},
	}
	cases := []struct {
		name     string
		after    res
		replaced []string
		want     []string
		wantNone bool
	}{
		{"no change prints nothing on stdout", before, nil, nil, true},
		{"a top-level field", func() res { r := before; r.URL = "https://b"; return r }(), nil, []string{"url  https://a  https://b"}, false},
		{"one nested key, the sibling unchanged", func() res {
			r := before
			r.Config = map[string]cfg{"ba1": {false, "all"}, "ba2": {false, "list"}}
			return r
		}(), nil, []string{"config.ba1.on  true  false"}, false},
		{"a scalar list as one value", func() res { r := before; r.Tags = []string{"A"}; return r }(), nil, []string{"tags  A, B  A"}, false},
		{"emptied list reads -", func() res { r := before; r.Tags = []string{}; return r }(), nil, []string{"tags  A, B  -"}, false},
		{"list of objects by index", func() res { r := before; r.List = []cfg{{true, "none"}}; return r }(), nil, []string{"list[0].mode  all  none"}, false},
		{"null to a value", func() res { r := before; r.Pointer = &x; return r }(), nil, []string{"pointer  -  x"}, false},
		{"a masked value the write replaces", before, []string{"key"}, []string{"key  ***  *** (new)"}, false},
		{"a field only one side has", func() res { r := before; r.Key = ""; return r }(), nil, []string{"key  ***  -"}, false},
		{"a replaced secret neither side shows", func() res { r := before; r.Key = ""; return r }(), []string{"secret"}, []string{"key  ***  -", "secret  -  *** (new)"}, false},
		{"empty string is not null", func() res { r := before; r.Name = ""; return r }(), nil, []string{`name  a  ""`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, out, errOut := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}
			require.NoError(t, f.RenderChanges(before, tc.after, tc.replaced...))
			if tc.wantNone {
				assert.Empty(t, out.String())
				assert.Equal(t, "No field would change.\n", errOut.String())
				return
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			require.Len(t, lines, len(tc.want)+1, out.String())
			assert.Equal(t, []string{"FIELD", "BEFORE", "AFTER"}, strings.Fields(lines[0]))
			for i, want := range tc.want {
				assert.Equal(t, strings.Fields(want), strings.Fields(lines[i+1]))
			}
		})
	}
}

// A server value in a dry-run table is stripped like any other human output; JSON keeps it.
func TestRenderChanges_StripsTerminalEscapes(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}
	evil := "a\x1b]8;;https://evil\x07b\u202e"
	require.NoError(t, f.RenderChanges(map[string]string{"name": "x"}, map[string]string{"name": evil}))
	require.NoError(t, f.RenderFields(map[string]string{"name": evil}))
	assert.NotContains(t, out.String(), "\x1b")
	assert.NotContains(t, out.String(), "\u202e")

	ios, _, out, _ = iostreams.Test()
	f = &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "json"}}
	require.NoError(t, f.RenderDryRun(DryRun{After: map[string]string{"name": evil}}, nil))
	assert.Contains(t, out.String(), `"would_refuse": []`, "an empty list, never null")
	var got DryRun
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, evil, got.After.(map[string]any)["name"])
}

// Values that print alike still compare by their JSON, so a change is never hidden.
func TestRenderChanges_ComparesTheJSONNotTheText(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after any
	}{
		{"null and empty string", map[string]any{"a": nil}, map[string]any{"a": ""}},
		{"number and string", map[string]any{"a": 1}, map[string]any{"a": "1"}},
		{"bool and string", map[string]any{"a": true}, map[string]any{"a": "true"}},
		{"two items and one with a comma", map[string]any{"a": []string{"A", "B"}}, map[string]any{"a": []string{"A, B"}}},
		{"empty list and missing", map[string]any{"a": []string{}}, map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, out, errOut := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}
			require.NoError(t, f.RenderChanges(tc.before, tc.after))
			assert.Contains(t, out.String(), "a ", "a row for the change")
			assert.NotContains(t, errOut.String(), "No field would change.")
		})
	}
}

// A narrow terminal must not cut the value the write sets.
func TestRenderChanges_NeverTruncates(t *testing.T) {
	long := "https://shop.example.com/a/very/long/webhook/path/that/does/not/fit"
	ios, _, out, _ := iostreams.Test()
	ios.SetStdoutTTY(true)
	ios.SetTerminalWidth(50)
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}
	require.NoError(t, f.RenderChanges(map[string]string{"url": "https://a"}, map[string]string{"url": long}))
	require.NoError(t, f.RenderFields(map[string]string{"url": long}))
	assert.Equal(t, 2, strings.Count(out.String(), long))
}

// An empty object on one side lists its fields as removed, never a row with no field name.
func TestRenderChanges_EmptyTopLevelObject(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}
	require.NoError(t, f.RenderChanges(map[string]any{"a": "x"}, map[string]any{}))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2, out.String())
	assert.Equal(t, []string{"a", "x", "-"}, strings.Fields(lines[1]))
}
