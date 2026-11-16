package root

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRoot(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
	return NewCmdRoot(f), out
}

func newTestRootLoggedIn(t *testing.T, profile string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	hosts := &config.Hosts{
		Active:   profile,
		Profiles: map[string]*config.Profile{profile: {Host: "my.sepay.vn"}},
	}
	f := &cmdutil.Factory{
		IOStreams:   ios,
		Global:      &cmdutil.GlobalOptions{},
		Credentials: func() (*auth.Manager, error) { return auth.NewManager(hosts), nil },
	}
	return NewCmdRoot(f), out
}

func TestRoot_WiresAllCommands(t *testing.T) {
	cmd, _ := newTestRoot(t)
	got := map[string]bool{}
	for _, c := range cmd.Commands() {
		got[c.Name()] = true
	}
	for _, name := range []string{
		"auth", "login", "logout", "version",
		"webhook", "transaction", "bank-account", "va",
		"quota", "paycode", "apikey", "sandbox",
		"pg", "einvoice", "bankhub", "api", "completion",
	} {
		assert.True(t, got[name], "missing top-level command %q", name)
	}
}

func TestRoot_StubsReturnNotImplemented(t *testing.T) {
	paths := [][]string{
		{"einvoice", "invoice", "delete", "x"},
		{"bankhub", "va", "list"},
		{"bankhub", "sandbox", "link-request", "accept", "1"},
	}
	for _, args := range paths {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd, _ := newTestRoot(t)
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not implemented")
			assert.Equal(t, cmdutil.ExitNotImplemented, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestRoot_UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing required flag", []string{"webhook", "listen"}},
		{"va issue needs bank-account", []string{"va", "issue"}},
		{"missing positional arg", []string{"paycode", "set"}},
		{"completion rejects unknown shell", []string{"completion", "tcsh"}},
		{"unknown command", []string{"nope"}},
		{"pg subcommand still validates -o", []string{"pg", "auth", "-o", "bogus"}},
		{"pg rejects production (sandbox-only)", []string{"pg", "auth", "--env", "production"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _ := newTestRoot(t)
			cmd.SetArgs(tc.args)
			err := cmd.ExecuteContext(context.Background())
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

// --json must match -o json even for errors cobra raises before PersistentPreRunE.
func TestRoot_MachineModeNeverPrintsProse(t *testing.T) {
	failures := map[string][]string{
		"too many args":      {"docs", "glossary", "extra"},
		"too few args":       {"transaction", "get"},
		"unknown flag":       {"transaction", "list", "--bogus"},
		"unknown command":    {"bogus"},
		"unknown subcommand": {"webhook", "lst"},
		"not implemented":    {"bankhub", "va", "list"},
	}
	for name, base := range failures {
		for _, machine := range [][]string{{"--json"}, {"-o", "json"}} {
			t.Run(name+" "+strings.Join(machine, " "), func(t *testing.T) {
				args := append(append([]string{}, base...), machine...)
				ios, _, out, errOut := iostreams.Test()
				f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}, RawArgs: args}
				cmd := NewCmdRoot(f)
				cmd.SetArgs(args)

				err := cmd.ExecuteContext(context.Background())
				require.Error(t, err)
				code := cmdutil.HandleError(f, err)

				var got struct {
					Status    string `json:"status"`
					ErrorCode string `json:"error_code"`
					Message   string `json:"message"`
					ExitCode  int    `json:"exit_code"`
				}
				require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
				assert.Equal(t, "error", got.Status)
				assert.Equal(t, int(code), got.ExitCode)
				assert.NotEmpty(t, got.ErrorCode)
				assert.NotEmpty(t, got.Message)
				assert.Empty(t, out.String(), "stdout stays data-only")
			})
		}
	}
}

// Cobra resolves the first match silently, so a shared name or alias would shadow a command.
func TestAliases_NoCollisionWithinGroup(t *testing.T) {
	cmd, _ := newTestRoot(t)
	var walk func(parent *cobra.Command)
	walk = func(parent *cobra.Command) {
		seen := map[string]string{} // token -> owning command name
		for _, c := range parent.Commands() {
			for _, tok := range append([]string{c.Name()}, c.Aliases...) {
				if owner, dup := seen[tok]; dup {
					t.Errorf("%q: %q clashes between %q and %q", parent.CommandPath(), tok, owner, c.Name())
				}
				seen[tok] = c.Name()
			}
			walk(c)
		}
	}
	walk(cmd)
}

// A value copied from one command's flag must mean the same thing on another: --va is a VA
// number everywhere and --va-xid an xid, or a create's xid lands in webhook test as a number.
func TestFlags_VAMeansTheSameEverywhere(t *testing.T) {
	cmd, _ := newTestRoot(t)
	want := map[string]string{"va": "number", "va-xid": "xid"}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for name, kind := range want {
			if fl := c.LocalFlags().Lookup(name); fl != nil && !fl.Hidden {
				assert.Contains(t, fl.Usage, kind, "%s --%s", c.CommandPath(), name)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)
}

func TestAliases_Resolve(t *testing.T) {
	cases := []struct {
		args, wantPath []string
	}{
		{[]string{"tx", "ls"}, []string{"transaction", "list"}},
		{[]string{"wh", "rm"}, []string{"webhook", "delete"}},
		{[]string{"ba", "new"}, []string{"bank-account", "create"}},
		{[]string{"bh", "co", "edit"}, []string{"company", "update"}},
		{[]string{"pg", "merchant", "show"}, []string{"merchant", "get"}},
		{[]string{"bankhub", "webhook", "get"}, []string{"webhook", "list"}},
		{[]string{"bankhub", "webhook", "show"}, []string{"webhook", "list"}},
		{[]string{"bankhub", "webhook", "ls"}, []string{"webhook", "list"}},
		{[]string{"auth", "sw"}, []string{"auth", "switch"}},
		{[]string{"auth", "ls"}, []string{"auth", "status"}},
		{[]string{"auth", "st"}, []string{"auth", "status"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd, _ := newTestRoot(t)
			found, _, err := cmd.Find(tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.wantPath[len(tc.wantPath)-1], found.Name())
			assert.Equal(t, tc.wantPath[len(tc.wantPath)-2], found.Parent().Name())
		})
	}
}

// Completion offers only Name(), so help must list aliases.
func TestAliases_ListedInSurface(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		contains []string
	}{
		{"root help", []string{"--help"}, []string{"transaction, tx, txn", "bank-account, ba", "webhook, wh"}},
		{"group help", []string{"webhook", "--help"}, []string{"list, ls", "delete, rm, del", "listen, tail"}},
		{"grouped help", []string{"bankhub", "--help"}, []string{"company, co", "link-token, lt"}},
		{"tree", []string{"--map=tree"}, []string{"webhook, wh", "  list, ls"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, out := newTestRoot(t)
			cmd.SetOut(out)
			cmd.SetArgs(tc.args)
			require.NoError(t, cmd.ExecuteContext(context.Background()))
			for _, want := range tc.contains {
				assert.Contains(t, out.String(), want)
			}
		})
	}
}

func TestAliases_ListColumnStaysAligned(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	column := -1
	for _, short := range []string{
		"Transactions on your bank accounts", // widest entry: transaction, tx, txn
		"Webhooks, and a local stream of their events",
		"Make an authenticated request to any SePay API endpoint", // no alias
	} {
		line := lineContaining(t, out.String(), short)
		at := strings.Index(line, short)
		if column == -1 {
			column = at
		}
		assert.Equal(t, column, at, "Short text misaligned: %q", line)
	}
}

func lineContaining(t *testing.T, s, want string) string {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line containing %q", want)
	return ""
}

func TestCompletion_GeneratesScript(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			cmd, out := newTestRoot(t)
			cmd.SetArgs([]string{"completion", shell})
			require.NoError(t, cmd.ExecuteContext(context.Background()))
			assert.NotEmpty(t, out.String())
		})
	}
}

// The root flag-error func rewrites pflag's InvalidValueError; a FlagError cause must keep its own text.
func TestRoot_RenamedFlagConflictKeepsItsMessage(t *testing.T) {
	cmd, _ := newTestRoot(t)
	cmd.SetArgs([]string{"transaction", "list", "--from", "2026-07-01", "--transaction-date-from", "2026-07-02"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Equal(t, "--from and --transaction-date-from are the same flag; pass one", err.Error())
}

func TestRoot_BindsTheEnvBannerBeforeTheFirstPrompt(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	t.Setenv("SEPAY_SANDBOX_API_URL", "http://127.0.0.1:1/v2")
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
	f.Config = config.Load
	prompttest.Use(t, f, prompttest.Step{Label: "Pay code prefix (2-5 letters)", Answer: "H1"})
	cmd := NewCmdRoot(f)
	cmd.SetArgs([]string{"paycode", "create"})
	require.Error(t, cmd.ExecuteContext(context.Background()), "the prefix answer is refused, after the banner")
	assert.Equal(t, "env: sandbox · host from SEPAY_SANDBOX_API_URL\n", errOut.String())
}

// The env banner reads AnnHost to name the hosts a command calls, so a command under a product noun
// that runs elsewhere must carry it; without it the banner names a host the command never calls.
func TestProductNouns_CommandsOffTheirNounsHostAreAnnotated(t *testing.T) {
	cmd, _ := newTestRoot(t)
	tests := map[string]string{
		"pg ipn listen":        cmdutil.HostAPI,
		"pg ipn get":           cmdutil.HostAPI,
		"pg ipn set":           cmdutil.HostAPI,
		"bankhub ipn listen":   cmdutil.HostAPI,
		"bankhub ipn get":      cmdutil.HostAPI,
		"bankhub ipn set":      cmdutil.HostAPI,
		"pg checkout init":     cmdutil.HostPGCheckout,
		"pg order list":        "",
		"bankhub company list": "",
	}
	for path, want := range tests {
		found, _, err := cmd.Find(strings.Fields(path))
		require.NoError(t, err, path)
		require.Equal(t, path, strings.TrimPrefix(found.CommandPath(), "sepay "), "%s must exist", path)
		assert.Equal(t, want, cmdutil.Annotation(found, cmdutil.AnnHost), path)
	}
}
