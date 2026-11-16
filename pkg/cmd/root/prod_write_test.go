package root

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// prodWrites lists one invocation of every production write: named ones carry the id of the
// resource they change, unnamed ones (creates, a paycode update without --structure) carry none.
type prodWriteCase = struct {
	path  string
	args  []string
	named bool
}

var prodWrites = []prodWriteCase{
	{"sepay api", []string{"api", "-X", "PUT", "/webhooks/" + goodXID, "-f", "name=x"}, true},
	{"sepay api", []string{"api", "-X", "DELETE", "/webhooks/" + goodXID}, true},
	{"sepay api", []string{"api", "-X", "POST", "/webhooks", "-f", "name=x"}, false},
	{"sepay api", []string{"api", "-X", "POST", "/bank-accounts/" + goodXID + "/orders", "-f", "amount=1"}, false},
	{"sepay bank-account update", []string{"bank-account", "update", goodXID, "--label", "x"}, true},
	{"sepay paycode create", []string{"paycode", "create", "HD", "--suffix-from", "3", "--suffix-to", "10", "--character-type", "NumberOnly", "--active"}, false},
	{"sepay paycode update", []string{"paycode", "update", "--structure", "DH", "--suffix-to", "9"}, true},
	{"sepay paycode update", []string{"paycode", "update", "--suffix-to", "9"}, false},
	{"sepay paycode update", []string{"paycode", "update", "--structure", " ", "--suffix-to", "9"}, false},
	{"sepay paycode update", []string{"paycode", "update", "--structure", "DH", "--disable"}, false},
	{"sepay webhook create", []string{"webhook", "create", "--url", "https://example.com/hook", "--bank-mode", "all"}, false},
	{"sepay webhook delete", []string{"webhook", "delete", goodXID}, true},
	{"sepay webhook replay", []string{"webhook", "replay", "tx-1", "--webhook", goodXID}, true},
	{"sepay webhook update", []string{"webhook", "update", goodXID, "--name", "x"}, true},
}

// prodWritePaths lists the commands prodWrites covers, once each.
func prodWritePaths() []string {
	var paths []string
	for _, w := range prodWrites {
		if !slices.Contains(paths, w.path) {
			paths = append(paths, w.path)
		}
	}
	return paths
}

const currentPaycode = `{"status":"success","data":{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"}],"version":"v1"}}`

// runProdWrite runs args against a server that answers the paycode read and counts every write.
func runProdWrite(t *testing.T, defaultEnv string, args []string, setup ...func(*cmdutil.Factory)) (writes, requests int32, err error) {
	t.Helper()
	var writeCount, requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Method != http.MethodGet {
			writeCount.Add(1)
		}
		if r.Method == http.MethodGet && r.URL.Path == "/company/paycode" {
			_, _ = w.Write([]byte(currentPaycode))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"internal_error","message":"test server"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())

	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
	f.Config = func() (*config.Config, error) {
		cfg, err := config.Load()
		if err == nil && defaultEnv != "" {
			cfg.Set("default_env", defaultEnv)
		}
		return cfg, err
	}
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
	err = cmd.ExecuteContext(context.Background())
	return writeCount.Load(), requestCount.Load(), err
}

// credentialWrites are the commands whose non-GET requests go to the OAuth host to manage the
// user's own sessions. They are outside the production write rule.
var credentialWrites = []string{
	"sepay auth login", "sepay auth logout", "sepay auth refresh", "sepay auth revoke",
	"sepay login", "sepay logout",
}

// sweepInvocations returns argument lists that reach the commands' requests without knowing them:
// bare, with one or two ids, each flag on its own (an id for a value), and each example line.
func sweepInvocations(c *cobra.Command) [][]string {
	base := strings.Fields(c.CommandPath())[1:]
	with := func(extra ...string) []string { return append(slices.Clone(base), extra...) }
	out := [][]string{with(), with(goodXID), with(goodXID, goodXID)}
	c.NonInheritedFlags().VisitAll(func(fl *pflag.Flag) {
		if fl.Name == "help" {
			return
		}
		if fl.Value.Type() == "bool" {
			out = append(out, with("--"+fl.Name), with(goodXID, "--"+fl.Name))
			return
		}
		out = append(out, with("--"+fl.Name, goodXID), with(goodXID, "--"+fl.Name, goodXID))
	})
	placeholder := regexp.MustCompile(`<[a-z-]+>`)
	for _, line := range strings.Split(c.Example, "\n") {
		line = placeholder.ReplaceAllString(strings.TrimSpace(line), goodXID)
		if !strings.HasPrefix(line, "sepay ") || strings.ContainsAny(line, "<>|$\"'") {
			continue
		}
		out = append(out, strings.Fields(line)[1:])
	}
	return out
}

// runSweep runs args unattended with --env production, and --yes when yes is set, logged in,
// against one fake API host and one fake OAuth host, and counts the non-GET requests each receives.
func runSweep(t *testing.T, args []string, yes bool) (apiWrites, oauthWrites int32, err error) {
	t.Helper()
	var apiCount, oauthCount atomic.Int32
	counting := func(n *atomic.Int32) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				n.Add(1)
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"internal_error","message":"test server"}`))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	apiSrv, oauthSrv := counting(&apiCount), counting(&oauthCount)
	dir := t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	keyring.MockInit()

	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}, RawArgs: args}
	f.Config = config.Load
	f.Credentials = func() (*auth.Manager, error) {
		hosts, err := config.LoadHostsFrom(dir)
		if err != nil {
			return nil, err
		}
		return auth.NewManager(hosts), nil
	}
	mgr, err := f.Credentials()
	require.NoError(t, err)
	_, err = mgr.Set("work", "my.sepay.vn", &auth.Credential{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}, []string{"read"}, false, "a@x.vn")
	require.NoError(t, err)
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(apiSrv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	f.NewOAuthClient = func(string) *auth.Client { return auth.NewClient(oauthSrv.URL, cmdutil.ClientID, nil) }
	noProduct := errors.New("product commands are sandbox only")
	f.ProductManager = func() (*product.Manager, error) { return nil, noProduct }
	f.NewPGClient = func() (*pg.Client, error) { return nil, noProduct }
	f.NewBankHubClient = func() (*bankhub.Client, error) { return nil, noProduct }
	f.NewEInvoiceClient = func() (*einvoice.Client, error) { return nil, noProduct }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := NewCmdRoot(f)
	args = append(slices.Clone(args), "--env", "production")
	if yes {
		args = append(args, "--yes")
	}
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err = cmd.ExecuteContext(ctx)
	return apiCount.Load(), oauthCount.Load(), err
}

// isProdWriteRefusal reports whether err is the production write gate's own refusal.
func isProdWriteRefusal(err error) bool {
	return err != nil && cmdutil.ExitCodeForError(err) == cmdutil.ExitUsage &&
		(strings.Contains(err.Error(), "on production needs confirmation") || strings.Contains(err.Error(), "names no single resource"))
}

// A production write is found by what it does, not by its scope: a command that writes, or that
// the production write gate refuses, must carry the annotation even without a :write scope.
func TestProdWrite_AnnotationMarksExactlyTheProductionWrites(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var annotated, apiWriters, oauthWriters, gated []string
	addOnce := func(list *[]string, path string) {
		if !slices.Contains(*list, path) {
			*list = append(*list, path)
		}
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() {
			path := c.CommandPath()
			if cmdutil.ConfirmsIn(c, config.EnvProduction) == cmdutil.ConfirmsTerminal {
				annotated = append(annotated, path)
			}
			sandboxOnly := cmdutil.Annotation(c, cmdutil.AnnEnvs) == "sandbox"
			for _, args := range sweepInvocations(c) {
				apiWrites, oauthWrites, _ := runSweep(t, args, true)
				if sandboxOnly {
					assert.Zero(t, apiWrites+oauthWrites, "sandbox-only %v sent a write on production", args)
					continue
				}
				if apiWrites > 0 {
					addOnce(&apiWriters, path)
				}
				if oauthWrites > 0 {
					addOnce(&oauthWriters, path)
				}
				// Without --yes the gate refuses before any read, so a write the --yes run did not reach is still found.
				if _, _, err := runSweep(t, args, false); isProdWriteRefusal(err) {
					addOnce(&gated, path)
				}
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(NewCmdRoot(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}))

	for _, path := range apiWriters {
		assert.Contains(t, annotated, path, "sends a write to the API host on production without the annotation")
	}
	for _, path := range gated {
		assert.Contains(t, annotated, path, "the production write gate refuses it, so it must carry the annotation")
	}
	assert.Subset(t, gated, []string{"sepay webhook create", "sepay paycode create"}, "the pass without --yes reaches the writes --yes cannot")
	assert.ElementsMatch(t, credentialWrites, oauthWriters, "a new non-GET to the OAuth host must be placed under the rule or listed here")
	assert.Subset(t, apiWriters, []string{"sepay api", "sepay webhook delete", "sepay webhook update"}, "the sweep reaches the writes it is meant to find")
	assert.ElementsMatch(t, annotated, prodWritePaths(), "every annotated command has a case in prodWrites")
}

// An agent learns the rule from --map=json and --help, so both state it on exactly the annotated commands.
func TestProdWrite_HelpAndMapStateTheRule(t *testing.T) {
	const helpLine = "On production a write runs only with --env production on the command line"
	annotated := prodWritePaths()

	m := commandMapIn(t, config.EnvProduction)
	var inMap []string
	for _, c := range m.Commands {
		if c.Confirms == cmdutil.ConfirmsTerminal {
			inMap = append(inMap, c.Path)
		}
	}
	assert.ElementsMatch(t, annotated, inMap)

	ios, _, _, _ := iostreams.Test()
	root := NewCmdRoot(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}})
	for _, path := range annotated {
		c, _, err := root.Find(strings.Fields(path)[1:])
		require.NoError(t, err)
		assert.NotContains(t, c.Example, "--env production", "%s: an example is copied as is, so it must not add the flag for the user", path)
	}

	const promptsAlways, promptsSome = "Prompts in sandbox too", "Some invocations prompt (see the note above)"
	sandboxConfirms := func(path string) string {
		c, _, err := root.Find(strings.Fields(path)[1:])
		require.NoError(t, err)
		return cmdutil.ConfirmsIn(c, config.EnvSandbox)
	}
	// One of each annotation form, so a reader that drops a form cannot pass by dropping the help line too.
	for path, want := range map[string]string{
		"sepay apikey delete": "always", "sepay webhook delete": "always",
		"sepay auth revoke": "conditional", "sepay api": "conditional", "sepay webhook create": "",
	} {
		assert.Equal(t, want, sandboxConfirms(path), path)
	}

	for _, c := range m.Commands {
		t.Run(c.Path, func(t *testing.T) {
			cmd, out := newTestRoot(t)
			cmd.SetOut(out)
			cmd.SetArgs(append(strings.Fields(c.Path)[1:], "--help"))
			require.NoError(t, cmd.ExecuteContext(context.Background()))
			if slices.Contains(annotated, c.Path) {
				assert.Contains(t, out.String(), helpLine)
				assert.NotContains(t, out.String(), "Use --env production", "the rule says not to add it, so nothing else may suggest it")
			} else {
				assert.NotContains(t, out.String(), helpLine)
			}
			switch sandboxConfirms(c.Path) {
			case "always":
				assert.Contains(t, out.String(), promptsAlways)
			case "conditional":
				assert.Contains(t, out.String(), promptsSome)
			default:
				assert.NotContains(t, out.String(), promptsAlways)
				assert.NotContains(t, out.String(), promptsSome)
			}
		})
	}
}

func TestProdWrite_UnattendedWithoutYesAsksForTheUser(t *testing.T) {
	for _, w := range prodWrites {
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			writes, _, err := runProdWrite(t, "", append(w.args, "--env", "production"))
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Contains(t, err.Error(), "ask the user to run this command in their own terminal")
			assert.NotContains(t, err.Error(), "--yes")
			assert.Zero(t, writes)
		})
	}
}

func TestProdWrite_DefaultEnvDoesNotStandInForEnvFlag(t *testing.T) {
	for _, w := range prodWrites {
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			writes, _, err := runProdWrite(t, config.EnvProduction, append(w.args, "--yes"))
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Contains(t, err.Error(), "default_env does not count")
			assert.Contains(t, err.Error(), "ask the user to run this command in their own terminal")
			assert.NotContains(t, err.Error(), "add --env", "an agent must not be told to add the flag itself")
			assert.Zero(t, writes)
		})
	}
}

// --force has its own terminal-only check; unattended, the production-write refusal still comes first.
func TestProdWrite_ForcedReplayUnattendedAsksForTheUser(t *testing.T) {
	force := []string{"webhook", "replay", "tx-1", "--webhook", goodXID, "--force"}
	for _, tc := range []struct {
		name       string
		defaultEnv string
		args       []string
		want       string
	}{
		{"without --yes", "", append(slices.Clone(force), "--env", "production"), "needs confirmation"},
		{"default_env production", config.EnvProduction, append(slices.Clone(force), "--yes"), "default_env does not count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes, _, err := runProdWrite(t, tc.defaultEnv, tc.args)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "ask the user to run this command in their own terminal")
			assert.NotContains(t, err.Error(), "--yes")
			assert.Zero(t, writes)
		})
	}
}

// At a terminal a missing --env production stops the write before its first question or request,
// including the bare invocations that open a form.
func TestProdWrite_TerminalRefusesBeforeAnyQuestion(t *testing.T) {
	invocations := [][]string{
		{"bank-account", "update", goodXID},
		{"webhook", "update", goodXID},
		{"webhook", "delete"},
		{"webhook", "create"},
		{"paycode", "create"},
		{"paycode", "update"},
	}
	for _, w := range prodWrites {
		invocations = append(invocations, w.args)
	}
	for _, args := range invocations {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, requests, err := runProdWrite(t, config.EnvProduction, args, func(f *cmdutil.Factory) {
				prompttest.Use(t, f)
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "add --env production")
			assert.Zero(t, requests)
		})
	}
}

// keyAtConfirm answers every confirm with one key: y, n, or enter, which takes the offered default.
type keyAtConfirm struct {
	*prompttest.Prompter
	key      string
	labels   []string
	defaults []bool
}

// Confirm implements cmdutil.Prompter.
func (p *keyAtConfirm) Confirm(label string, def bool) (bool, error) {
	p.labels = append(p.labels, label)
	p.defaults = append(p.defaults, def)
	switch p.key {
	case "y":
		return true, nil
	case "n":
		return false, nil
	}
	return def, nil
}

// At a terminal the answer to the production confirm decides a named write: n and Enter send
// nothing, y sends the one write.
func TestProdWrite_TerminalAnswerDecidesTheWrite(t *testing.T) {
	cases := []struct {
		key    string
		writes int32
	}{
		{"n", 0},
		{"enter", 0},
		{"y", 1},
	}
	for _, w := range prodWrites {
		if !w.named {
			continue
		}
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.key, func(t *testing.T) {
					var writes atomic.Int32
					// Answers the reads that come before the confirm, replay's preview among them.
					srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
						switch {
						case r.Method != http.MethodGet:
							writes.Add(1)
						case r.URL.Path == "/company/paycode":
							_, _ = rw.Write([]byte(currentPaycode))
							return
						case strings.HasPrefix(r.URL.Path, "/transactions/"):
							_, _ = rw.Write([]byte(`{"status":"success","data":{"id":"tx-1"}}`))
							return
						case r.URL.Path == "/webhook-logs":
							_, _ = rw.Write([]byte(`{"status":"success","data":[]}`))
							return
						}
						rw.WriteHeader(http.StatusInternalServerError)
						_, _ = rw.Write([]byte(`{"status":"error","error_code":"internal_error","message":"test server"}`))
					}))
					t.Cleanup(srv.Close)

					var confirm *keyAtConfirm
					_, _, err := runProdWrite(t, "", append(w.args, "--env", "production"), func(f *cmdutil.Factory) {
						confirm = &keyAtConfirm{Prompter: prompttest.Use(t, f), key: tc.key}
						f.Prompter = confirm
						f.NewAPIClient = func() (*api.Client, error) {
							c := api.NewClient(srv.URL, "test")
							c.MaxRetries = 0
							return c, nil
						}
					})
					require.Len(t, confirm.labels, 1, "only the production confirm is asked")
					assert.True(t, strings.HasSuffix(confirm.labels[0], " on production?"), confirm.labels[0])
					assert.False(t, confirm.defaults[0], "Enter must mean no")
					assert.Equal(t, tc.writes, writes.Load())
					if tc.writes == 0 {
						require.Error(t, err)
						assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
						assert.True(t, strings.HasSuffix(err.Error(), " cancelled"), err.Error())
					}
				})
			}
		})
	}
}

func TestProdWrite_YesNeedsTheResourceNamed(t *testing.T) {
	for _, w := range prodWrites {
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			writes, requests, err := runProdWrite(t, "", append(w.args, "--env", "production", "--yes"))
			if w.named {
				if w.path == "sepay webhook replay" {
					// Its preview reads the transaction first; TestReplay_Production* cover the send.
					return
				}
				assert.Equal(t, int32(1), writes, "a named write with --env production --yes is sent")
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Contains(t, err.Error(), "names no single resource")
			assert.Zero(t, requests, "refused before the reads that precede the confirm")
			assert.Zero(t, writes)
		})
	}
}

// A write that names its resource, run unattended with the id left out or blank, is refused before
// the picker or the preview could read anything.
func TestProdWrite_YesWithoutTheIDSendsNothing(t *testing.T) {
	cases := []struct {
		path string
		args []string
	}{
		{"sepay api", []string{"api", "-X", "PUT", "/webhooks", "-f", "name=x"}},
		{"sepay api", []string{"api", "-X", "DELETE", "/webhooks/"}},
		{"sepay bank-account update", []string{"bank-account", "update", "--label", "x"}},
		{"sepay bank-account update", []string{"bank-account", "update", " ", "--label", "x"}},
		{"sepay paycode update", []string{"paycode", "update", "--suffix-to", "9"}},
		{"sepay webhook delete", []string{"webhook", "delete"}},
		{"sepay webhook delete", []string{"webhook", "delete", " "}},
		{"sepay webhook replay", []string{"webhook", "replay"}},
		{"sepay webhook replay", []string{"webhook", "replay", "--webhook", goodXID}},
		{"sepay webhook replay", []string{"webhook", "replay", " ", "--webhook", goodXID}},
		{"sepay webhook update", []string{"webhook", "update", "--name", "x"}},
		{"sepay webhook update", []string{"webhook", "update", " ", "--name", "x"}},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.path] = true
	}
	for _, w := range prodWrites {
		if w.named {
			assert.True(t, covered[w.path], "%s names its resource but has no case without the id", w.path)
		}
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			writes, requests, err := runProdWrite(t, "", append(slices.Clone(tc.args), "--env", "production", "--yes"))
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Contains(t, err.Error(), "names no single resource")
			assert.Contains(t, err.Error(), "ask the user to run this command in their own terminal")
			assert.Zero(t, requests)
			assert.Zero(t, writes)
		})
	}
}

// confirms states the sandbox prompt and the production rule separately; one must not stand in
// for the other, or the map stops saying which writes need --yes unattended in sandbox.
func TestProdWrite_ConfirmsStillDeclaresTheSandboxPrompt(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	root := NewCmdRoot(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}})
	for _, w := range prodWrites {
		t.Run(strings.Join(w.args, " "), func(t *testing.T) {
			_, _, err := runProdWrite(t, "", w.args)
			prompts := err != nil && strings.Contains(err.Error(), "requires confirmation; pass --yes")

			cmd, _, findErr := root.Find(w.args)
			require.NoError(t, findErr)
			confirms := cmdutil.ConfirmsIn(cmd, config.EnvSandbox)
			if prompts {
				assert.Contains(t, []string{"always", "conditional"}, confirms, "prompts in sandbox, so --map must say so")
			} else {
				assert.NotEqual(t, "always", confirms, "does not prompt in sandbox")
			}
		})
	}
}

// A malformed invocation reports its own usage error, not the production gate's.
func TestProdWrite_FlagConflictComesBeforeTheGate(t *testing.T) {
	_, requests, err := runProdWrite(t, "", []string{"paycode", "update", "--enable", "--disable", "--env", "production", "--yes"})
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "cannot use --enable and --disable together")
	assert.Zero(t, requests)
}

func TestProdWrite_APIRefusalNeverPrintsAQueryValue(t *testing.T) {
	const secret = "s3cretTOKEN"
	cases := []struct {
		name, defaultEnv string
		args             []string
	}{
		{"unattended", "", []string{"api", "-X", "POST", "/webhooks?access_token=" + secret, "--env", "production"}},
		{"default_env", config.EnvProduction, []string{"api", "-X", "POST", "/webhooks?access_token=" + secret, "--yes"}},
		{"no named resource", "", []string{"api", "-X", "POST", "/webhooks?access_token=" + secret, "--env", "production", "--yes"}},
		{"bare parameter", "", []string{"api", "-X", "PUT", "/webhooks/" + goodXID + "?" + secret, "--env", "production"}},
		{"pasted URL with userinfo", "", []string{"api", "-X", "POST", "https://u:" + secret + "@userapi.sepay.vn/v2/webhooks?a=1", "--env", "production"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writes, _, err := runProdWrite(t, tc.defaultEnv, tc.args)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), secret)
			assert.Contains(t, err.Error(), "?")
			assert.Zero(t, writes)
		})
	}
}

func commandMapIn(t *testing.T, env string) commandMap {
	t.Helper()
	cmd, out := newTestRoot(t)
	cmd.SetArgs([]string{"--map=json", "--env", env})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.NotContains(t, out.String(), "prod_confirm", "the production rule is a value of confirms, not a key of its own")
	var m commandMap
	require.NoError(t, json.Unmarshal(out.Bytes(), &m))
	require.Equal(t, env, m.ActiveEnv)
	return m
}

// --map=json prints confirms for the env it runs in: one word, never the per-env annotation.
func TestMap_ConfirmsIsTheActiveEnvsValue(t *testing.T) {
	want := map[string]map[string]string{
		config.EnvSandbox: {
			"sepay webhook create": "", "sepay webhook update": "", "sepay bank-account update": "",
			"sepay paycode create": "", "sepay paycode update": "",
			"sepay webhook delete": "always", "sepay webhook replay": "always",
			"sepay api": "conditional", "sepay auth revoke": "conditional", "sepay logout": "conditional",
		},
		config.EnvProduction: {
			"sepay webhook create": "terminal", "sepay webhook update": "terminal", "sepay bank-account update": "terminal",
			"sepay paycode create": "terminal", "sepay paycode update": "terminal",
			"sepay webhook delete": "terminal", "sepay webhook replay": "terminal",
			"sepay api": "terminal", "sepay auth revoke": "conditional", "sepay logout": "conditional",
		},
	}
	for env, byPath := range want {
		t.Run(env, func(t *testing.T) {
			got := map[string]string{}
			for _, c := range commandMapIn(t, env).Commands {
				assert.NotContains(t, c.Confirms, "=", c.Path)
				got[c.Path] = c.Confirms
			}
			for path, confirms := range byPath {
				assert.Contains(t, got, path)
				assert.Equal(t, confirms, got[path], path)
			}
		})
	}
}
