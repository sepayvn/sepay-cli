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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMap_Paths_ListsOnlyRunnableCommands(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=paths"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	paths := strings.Split(strings.TrimSpace(out.String()), "\n")
	assert.Contains(t, paths, "sepay webhook listen")
	assert.NotContains(t, paths, "sepay glossary", "a help topic is not a command")
	assert.Contains(t, paths, "sepay apikey list")
	assert.NotContains(t, paths, "sepay einvoice invoice delete")
}

// A logout that leaves no active profile means every command 401s.
func TestMap_JSON_AuthenticatedFollowsTheResolvedProfile(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	hosts := &config.Hosts{
		Active:   "",
		Profiles: map[string]*config.Profile{"work": {Host: "my.sepay.vn"}},
	}
	f := &cmdutil.Factory{
		IOStreams:   ios,
		Global:      &cmdutil.GlobalOptions{},
		Credentials: func() (*auth.Manager, error) { return auth.NewManager(hosts), nil },
	}
	cmd := NewCmdRoot(f)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))
	assert.False(t, m.Authenticated, "a stored profile that is not active cannot authenticate a request")
	assert.Empty(t, m.ActiveProfile)
}

func TestMap_JSON_CarriesCapabilityMetadata(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))
	assert.Equal(t, "sandbox", m.ActiveEnv)

	byPath := map[string]commandInfo{}
	for _, c := range m.Commands {
		byPath[c.Path] = c
	}

	stub, ok := byPath["sepay einvoice invoice delete"]
	require.True(t, ok)
	assert.False(t, stub.Implemented)

	revoke, ok := byPath["sepay va revoke"]
	require.True(t, ok)
	assert.True(t, revoke.Implemented)
	assert.Equal(t, "sandbox", revoke.Envs)
	assert.Equal(t, "irreversible", revoke.Destructive)

	assert.Equal(t, "sandbox", byPath["sepay pg order cancel"].Envs)
	assert.Equal(t, "both", byPath["sepay transaction list"].Envs)

	listen := byPath["sepay webhook listen"]
	assert.Equal(t, "sandbox", listen.Envs)
	var flags []string
	for _, f := range listen.Flags {
		flags = append(flags, f.Name)
	}
	assert.Contains(t, flags, "forward")
}

func TestMap_RejectsUnknownFormat(t *testing.T) {
	cmd, _ := newTestRoot(t)
	cmd.SetArgs([]string{"--map=xml"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestHelp_CarriesAgentGuidance(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		contains []string
		absent   []string
	}{
		{
			name:     "root",
			args:     []string{"--help"},
			contains: []string{"[Agent guidance]", "sepay help glossary", "sepay --map=paths"},
		},
		{
			name: "sandbox-only write states it, and drops the production-wave lines",
			args: []string{"va", "revoke", "--help"},
			contains: []string{
				"Sandbox-only: this does not work on production.",
				"Irreversible:",
			},
			absent: []string{"needs the \"va:write\" scope"},
		},
		{
			name:     "production-capable write names its scope",
			args:     []string{"webhook", "create", "--help"},
			contains: []string{`needs the "webhook:write" scope, granted at ` + "`sepay login`"},
			absent:   []string{"Sandbox-only", "auth refresh"},
		},
		{
			name: "pg order read runs on production and says where its scope comes from",
			args: []string{"pg", "order", "list", "--help"},
			contains: []string{
				`needs the "pg:read" scope, granted at ` + "`sepay login`",
				"Every pg command except `pg order list` and `pg order get` is sandbox-only.",
			},
			absent: []string{"Sandbox-only: this does not work on production.", "auth refresh"},
		},
		{
			name:     "other pg commands stay sandbox-only",
			args:     []string{"pg", "order", "cancel", "--help"},
			contains: []string{"Sandbox-only: this does not work on production."},
			absent:   []string{"pg:read"},
		},
		{
			name: "einvoice invoice read runs on production and says where its scope comes from",
			args: []string{"einvoice", "invoice", "get", "--help"},
			contains: []string{
				`needs the "einvoice:read" scope, granted at ` + "`sepay login`",
				"Every einvoice command except `einvoice invoice list` and `einvoice invoice get` is sandbox-only.",
			},
			absent: []string{"Sandbox-only: this does not work on production.", "auth refresh"},
		},
		{
			name:     "other einvoice commands stay sandbox-only",
			args:     []string{"einvoice", "invoice", "issue", "--help"},
			contains: []string{"Sandbox-only: this does not work on production."},
			absent:   []string{"einvoice:read"},
		},
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
			for _, unwanted := range tc.absent {
				assert.NotContains(t, out.String(), unwanted)
			}
		})
	}
}

func TestHelp_ReportsLoginState(t *testing.T) {
	const warning = "Not logged in:"

	cmd, out := newTestRoot(t)
	cmd.SetOut(out)
	cmd.SetArgs([]string{"webhook", "create", "--help"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.Contains(t, out.String(), warning)
	assert.Contains(t, out.String(), moreGuidance)

	loggedIn, outLoggedIn := newTestRootLoggedIn(t, "default")
	loggedIn.SetOut(outLoggedIn)
	loggedIn.SetArgs([]string{"webhook", "create", "--help"})
	require.NoError(t, loggedIn.ExecuteContext(context.Background()))
	assert.NotContains(t, outLoggedIn.String(), warning)
}

func TestMap_JSON_ReportsLoginState(t *testing.T) {
	decode := func(t *testing.T, out *bytes.Buffer) commandMap {
		t.Helper()
		var m commandMap
		require.NoError(t, json.Unmarshal(out.Bytes(), &m))
		return m
	}

	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	m := decode(t, out)
	assert.False(t, m.Authenticated)
	assert.Empty(t, m.ActiveProfile)

	loggedIn, outLoggedIn := newTestRootLoggedIn(t, "work")
	loggedIn.SetArgs([]string{"--map=json"})
	require.NoError(t, loggedIn.ExecuteContext(context.Background()))
	m = decode(t, outLoggedIn)
	assert.True(t, m.Authenticated)
	assert.Equal(t, "work", m.ActiveProfile)
}

func TestRoot_BareInvocationPrintsHelp(t *testing.T) {
	cmd, _ := newTestRoot(t)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.Contains(t, out.String(), "Core Commands:")
}

func TestAnnotations_StayWithinTheirVocabulary(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))

	scopes := map[string]bool{
		"read": true, "webhook:write": true, "bank-account:write": true,
		"paycode:write": true, "pg:read": true, "einvoice:read": true,
	}
	for _, c := range m.Commands {
		if c.Destructive != "" {
			assert.Equal(t, "irreversible", c.Destructive, "%s: destructive has one value", c.Path)
		}
		assert.Contains(t, []string{"sandbox", "both"}, c.Envs, "%s: unknown envs", c.Path)
		if c.Scope != "" {
			assert.True(t, scopes[c.Scope], "%s: %q is not a scope the CLI can request", c.Path, c.Scope)
			// Scopes gate production only, so a sandbox-only command must not declare one.
			assert.NotEqual(t, "sandbox", c.Envs, "%s: sandbox-only command declares a production scope", c.Path)
		}
	}
}

// A --json caller would read help text as data.
func TestRoot_UnknownSubcommandIsAUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"webhook", "nosuchsub"},
		{"bank-account", "lst"},
		{"auth", "nope"},
		{"pg", "order", "bogus"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd, out := newTestRoot(t)
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(context.Background())
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "unknown command")
			assert.Empty(t, out.String(), "help must not go to stdout")
		})
	}
}

func TestRoot_GroupWithNoArgsPrintsHelp(t *testing.T) {
	cmd, _ := newTestRoot(t)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"webhook"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.Contains(t, out.String(), "Available Commands:")
}

// A renamed flag's old spelling is hidden from help but still listed for agents, pointing at the new one.
func TestMap_JSON_ListsOldFlagSpellings(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))

	flags := map[string]flagInfo{}
	for _, c := range m.Commands {
		if c.Path == "sepay transaction list" {
			for _, fl := range c.Flags {
				flags[fl.Name] = fl
			}
		}
	}
	assert.Equal(t, "from", flags["transaction-date-from"].AliasOf)
	assert.Equal(t, "search", flags["q"].AliasOf)
	assert.Empty(t, flags["from"].AliasOf)
}

// pg order list/get read production too; every other pg command stays sandbox-only.
func TestMap_PGOrderReadsAreTheOnlyProductionPGCommands(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))

	reads := map[string]bool{"sepay pg order list": true, "sepay pg order get": true}
	seen := 0
	for _, c := range m.Commands {
		if !strings.HasPrefix(c.Path, "sepay pg ") {
			continue
		}
		if reads[c.Path] {
			seen++
			assert.Equal(t, "both", c.Envs, c.Path)
			assert.Equal(t, "pg:read", c.Scope, c.Path)
			assert.Equal(t, "A gateway order notifies over IPN, so `sepay webhook listen` never carries one — use `sepay pg ipn listen`.", c.Guidance, c.Path)
			continue
		}
		assert.Equal(t, "sandbox", c.Envs, c.Path)
	}
	assert.Equal(t, len(reads), seen)
}

// einvoice invoice list/get read production too; every other einvoice command stays sandbox-only.
func TestMap_InvoiceReadsAreTheOnlyProductionEInvoiceCommands(t *testing.T) {
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))

	reads := map[string]bool{"sepay einvoice invoice list": true, "sepay einvoice invoice get": true}
	seen := 0
	for _, c := range m.Commands {
		if !strings.HasPrefix(c.Path, "sepay einvoice ") {
			continue
		}
		if reads[c.Path] {
			seen++
			assert.Equal(t, "both", c.Envs, c.Path)
			assert.Equal(t, "einvoice:read", c.Scope, c.Path)
			continue
		}
		assert.Equal(t, "sandbox", c.Envs, c.Path)
	}
	assert.Equal(t, len(reads), seen)
}

// A sentence a group's note already carries is not printed again for its subcommand.
func TestHelp_GuidanceDoesNotRepeatASentence(t *testing.T) {
	const ipn = "A gateway order notifies over IPN, so `sepay webhook listen` never carries one"
	for _, args := range [][]string{{"pg", "order", "list", "--help"}, {"pg", "order", "get", "--help"}, {"pg", "order", "cancel", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd, out := newTestRoot(t)
			cmd.SetOut(out)
			cmd.SetArgs(args)
			require.NoError(t, cmd.ExecuteContext(context.Background()))
			assert.Equal(t, 1, strings.Count(out.String(), ipn), out.String())
		})
	}
}

func TestWithoutRepeats(t *testing.T) {
	assert.Equal(t, []string{"A. B.", "C."}, withoutRepeats([]string{"A. B.", "B.", "C."}))
	assert.Equal(t, []string{"A. B.", "C."}, withoutRepeats([]string{"B.", "A. B.", "C."}), "a note a later, longer note carries")
	assert.Equal(t, []string{"A."}, withoutRepeats([]string{"A.", "A."}))
	assert.Equal(t, []string{"A.", "C."}, withoutRepeats([]string{"A.", "C.", "A."}), "the first of two equal notes stays in place")
	assert.Equal(t, []string{"A.", "B."}, withoutRepeats([]string{"A.", "B."}))
	assert.Empty(t, withoutRepeats(nil))
}
