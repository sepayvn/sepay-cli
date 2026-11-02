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
		"webhook", "transaction", "report", "bank-account", "va",
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

func TestAliases_Resolve(t *testing.T) {
	cases := []struct {
		args, wantPath []string
	}{
		{[]string{"tx", "ls"}, []string{"transaction", "list"}},
		{[]string{"wh", "rm"}, []string{"webhook", "delete"}},
		{[]string{"ba", "new"}, []string{"bank-account", "create"}},
		{[]string{"bh", "co", "update"}, []string{"company", "edit"}},
		{[]string{"auth", "sw"}, []string{"auth", "switch"}},
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
		"List and inspect transactions", // widest entry: transaction, tx, txn
		"Manage webhooks and stream events to a local handler",
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
