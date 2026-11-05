package cmdutil

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func promptFactory() *Factory {
	ios, _, _, _ := iostreams.Test()
	return &Factory{IOStreams: ios, Global: &GlobalOptions{}}
}

func TestInputOrFlag_ReturnsValue(t *testing.T) {
	got, err := promptFactory().InputOrFlag("https://x.test", "url", "URL")
	require.NoError(t, err)
	assert.Equal(t, "https://x.test", got)
}

func TestInputOrFlag_NonInteractiveMissing(t *testing.T) {
	_, err := promptFactory().InputOrFlag("", "url", "URL")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--url is required")
}

func TestInputOrArg_NonInteractiveMissing(t *testing.T) {
	_, err := promptFactory().InputOrArg("", "a link token", "Link token")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
	assert.Equal(t, "a link token is required", err.Error())
	assert.NotContains(t, err.Error(), "--")
}

func TestSelectOrFlag_NonInteractiveMissing(t *testing.T) {
	_, err := promptFactory().SelectOrFlag("", "connection", "Connection", []string{"sms", "api"})
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
	assert.Contains(t, err.Error(), "sms, api")
}

func TestConfirm_YesSkips(t *testing.T) {
	f := promptFactory()
	f.Global.Yes = true
	ok, err := f.Confirm("delete this?")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestConfirm_NonInteractiveNeedsYes(t *testing.T) {
	_, err := promptFactory().Confirm("delete this?")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--yes")
}

func TestConfirmProdWrite_SandboxNoOp(t *testing.T) {
	f := promptFactory()
	f.Global.Env = "sandbox"
	require.NoError(t, f.ConfirmProdWrite("create webhook"))
}

func TestConfirmProdWrite_ProdNeedsConfirm(t *testing.T) {
	f := promptFactory() // non-interactive, no --yes
	f.Global.Env = "production"
	err := f.ConfirmProdWrite("create webhook")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
}

func TestConfirmProdWrite_ProdYesProceedsWithBanner(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: "production", Yes: true}}
	require.NoError(t, f.ConfirmProdWrite("create webhook"))
	assert.Contains(t, errOut.String(), "live")
	assert.Contains(t, errOut.String(), "PRODUCTION")
}

func TestConfirmProdWrite_MachineOutputKeepsStderrForTheErrorObject(t *testing.T) {
	for _, output := range []string{"json", "yaml"} {
		ios, _, out, errOut := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: "production", Yes: true, Output: output}}
		require.NoError(t, f.ConfirmProdWrite("create webhook"))
		assert.Empty(t, errOut.String(), output)
		assert.Empty(t, out.String(), output)
	}
}

// The production warning goes where the question it introduces is drawn; under --yes nothing
// is asked, so it stays on stderr and stdout keeps only data.
func TestConfirmProdWrite_WarningFollowsThePrompt(t *testing.T) {
	for _, yes := range []bool{false, true} {
		ios, in, out, errOut := iostreams.Test()
		ios.SetStdinTTY(true)
		ios.SetStdoutTTY(true)
		in.WriteString("y")
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: "production", Yes: yes}}
		require.NoError(t, f.ConfirmProdWrite("create webhook"))
		if yes {
			assert.Contains(t, errOut.String(), "! live: create webhook targets PRODUCTION")
			assert.Empty(t, out.String())
		} else {
			assert.Contains(t, out.String(), "! live: create webhook targets PRODUCTION")
			assert.Empty(t, errOut.String())
		}
	}
}

func TestConfirmProdWrite_EnvBannerComesBeforeTheWarning(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, _, errOut := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	ios.SetStderrTTY(true)
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table", Env: "production"}, Prompter: recordingPrompter{errOut}}
	f.Config = config.Load
	root := &cobra.Command{Use: "sepay"}
	noun := &cobra.Command{Use: "apikey"}
	verb := &cobra.Command{Use: "create"}
	root.AddCommand(noun)
	noun.AddCommand(verb)
	f.BindCommand(verb)

	require.NoError(t, f.ConfirmProdWrite("create API key"))
	assert.Equal(t, "env: production\n! live: create API key targets PRODUCTION\n", errOut.String())
}

func TestSecretFromFlag_WarnsOnlyForAnInlineValue(t *testing.T) {
	cases := []struct {
		name, val string
		stdin     string
		warns     bool
	}{
		{name: "inline", val: "s3cret", warns: true},
		{name: "stdin sentinel", val: "-", stdin: "s3cret\n", warns: false},
		{name: "empty means auth off", val: "", warns: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ios, in, _, errOut := iostreams.Test()
			in.WriteString(c.stdin)
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

			got, err := f.SecretFromFlag(c.val, "api-key")
			require.NoError(t, err)
			if c.val == "-" {
				assert.Equal(t, "s3cret", got)
			} else {
				assert.Equal(t, c.val, got)
			}

			if c.warns {
				assert.Contains(t, errOut.String(), "--api-key passed inline is visible in shell history")
			} else {
				assert.Empty(t, errOut.String())
			}
		})
	}
}

func TestSecretFromFlag_PassesThroughAnOrdinaryValue(t *testing.T) {
	got, err := promptFactory().SecretFromFlag("s3cret", "secret")
	require.NoError(t, err)
	assert.Equal(t, "s3cret", got)
}

func TestSecretFromFlag_EmptyIsNotTheStdinSentinel(t *testing.T) {
	got, err := promptFactory().SecretFromFlag("", "api-key")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSecretFromFlag_ReadsAndTrimsStdin(t *testing.T) {
	ios, in, _, _ := iostreams.Test()
	in.WriteString("  piped-secret\n")
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	got, err := f.SecretFromFlag("-", "secret")
	require.NoError(t, err)
	assert.Equal(t, "piped-secret", got)
}

func TestSecretFromFlag_EmptyStdinIsAUsageError(t *testing.T) {
	_, err := promptFactory().SecretFromFlag("-", "secret")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
}

func TestSecretFromFlag_SecondDashNamesTheFlagThatReadStdin(t *testing.T) {
	ios, in, _, _ := iostreams.Test()
	in.WriteString("k")
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	got, err := f.SecretFromFlag("-", "secret")
	require.NoError(t, err)
	assert.Equal(t, "k", got)

	_, err = f.SecretFromFlag("-", "oauth2-client-secret")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--secret already did")
	assert.NotContains(t, err.Error(), "carried no secret", "must not blame the pipe it drained")
}

func TestSecretFromFlag_BoundsThePipe(t *testing.T) {
	ios, in, _, _ := iostreams.Test()
	in.WriteString(strings.Repeat("x", maxSecretBytes+500))
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	got, err := f.SecretFromFlag("-", "secret")
	require.NoError(t, err)
	assert.Len(t, got, maxSecretBytes)
}

func TestSecretFromFlag_RefusesATerminal(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	_, err := f.SecretFromFlag("-", "secret")
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err))
	assert.Contains(t, err.Error(), "stdin is a terminal")
	assert.Contains(t, err.Error(), "drop --secret")
}

func ttyFactory(keys string) (*Factory, *bytes.Buffer) {
	ios, in, out, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	ios.SetStderrTTY(true)
	in.WriteString(keys)
	return &Factory{IOStreams: ios, Global: &GlobalOptions{}}, out
}

func TestPrompts_MoveToStdoutWhenStderrIsRedirected(t *testing.T) {
	for _, stderrTTY := range []bool{true, false} {
		ios, in, out, errOut := iostreams.Test()
		ios.SetStdinTTY(true)
		ios.SetStdoutTTY(true)
		ios.SetStderrTTY(stderrTTY)
		in.WriteString("y")
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

		ok, err := f.Confirm("delete this?")
		require.NoError(t, err)
		assert.True(t, ok)
		if stderrTTY {
			assert.Empty(t, out.String(), "stdout carries only data")
			assert.NotEmpty(t, errOut.String())
		} else {
			assert.Empty(t, errOut.String())
			assert.NotEmpty(t, out.String(), "a redirected stderr must not hide the prompt")
		}
	}
}

func TestPrompts_ReturnTheAnswerAndKeepStdoutClean(t *testing.T) {
	choices := []PromptChoice{{Label: "Main", Value: "ba_1"}, {Label: "Main", Value: "ba_2"}}

	f, out := ttyFactory("\x1b[B\r")
	got, err := f.SelectChoiceOrFlag("", "bank-account", "Bank account", choices)
	require.NoError(t, err)
	assert.Equal(t, "ba_2", got, "a repeated label still returns the row picked")
	assert.Empty(t, out.String(), "the prompt draws on stderr; stdout carries only data")

	f, _ = ttyFactory("\r")
	got, err = f.InputWithDefault("Name", "shop\x1b[2J")
	require.NoError(t, err)
	assert.Equal(t, "shop\x1b[2J", got, "Enter keeps the caller's default, not its sanitised display")

	f, _ = ttyFactory("y")
	ok, err := f.Confirm("delete this?")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestPrompts_CtrlCIsErrInterrupted(t *testing.T) {
	f, _ := ttyFactory("\x03")
	_, err := f.SelectOrFlag("", "event", "Event type", []string{"in", "out"})
	require.ErrorIs(t, err, ErrInterrupted)
	assert.Equal(t, ExitInterrupted, ExitCodeForError(err))
}

type recordingPrompter struct{ errOut *bytes.Buffer }

func (p recordingPrompter) Select(string, []string, int) (int, error)            { return 0, nil }
func (p recordingPrompter) MultiSelect(string, []string, []int) ([]int, error)   { return nil, nil }
func (p recordingPrompter) Password(string) (string, error)                      { return "", nil }
func (p recordingPrompter) Confirm(string, bool) (bool, error)                   { return true, nil }
func (p recordingPrompter) Accounts(string, []FormAccount) ([]FormAnswer, error) { return nil, nil }
func (p recordingPrompter) Input(label, _ string) (string, error) {
	p.errOut.WriteString("? " + label + "\n")
	return "x", nil
}

func TestPrompts_EnvBannerComesFirst(t *testing.T) {
	tests := []struct {
		noun  string
		set   string
		bound string // a noun bound earlier on the same Factory
		want  string
	}{
		{"paycode", "SEPAY_SANDBOX_API_URL", "", "env: sandbox · host from SEPAY_SANDBOX_API_URL\n? Prefix\n"},
		{"paycode", "", "", "env: sandbox\n? Prefix\n"},
		{"bankhub", "SEPAY_SANDBOX_BANKHUB_API_URL", "", "env: sandbox · host from SEPAY_SANDBOX_BANKHUB_API_URL\n? Prefix\n"},
		{"bankhub", "SEPAY_SANDBOX_API_URL", "", "env: sandbox · API host from SEPAY_SANDBOX_API_URL\n? Prefix\n"},
		{"pg", "SEPAY_SANDBOX_API_URL", "", "env: sandbox · API host from SEPAY_SANDBOX_API_URL\n? Prefix\n"},
		{"einvoice", "SEPAY_SANDBOX_API_URL", "", "env: sandbox · API host from SEPAY_SANDBOX_API_URL\n? Prefix\n"},
		{"paycode", "SEPAY_SANDBOX_API_URL", "bankhub", "env: sandbox · host from SEPAY_SANDBOX_API_URL\n? Prefix\n"},
		{"config", "SEPAY_SANDBOX_API_URL", "", "? Prefix\n"},
	}
	for _, tt := range tests {
		name := tt.noun + " " + tt.set
		if tt.bound != "" {
			name += " after " + tt.bound
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			t.Setenv("SEPAY_SANDBOX_API_URL", "")
			t.Setenv("SEPAY_SANDBOX_BANKHUB_API_URL", "")
			if tt.set != "" {
				t.Setenv(tt.set, "http://127.0.0.1:1")
			}
			ios, _, _, errOut := iostreams.Test()
			ios.SetStdinTTY(true)
			ios.SetStdoutTTY(true)
			ios.SetStderrTTY(true)
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}, Prompter: recordingPrompter{errOut}}
			f.Config = config.Load

			bind := func(name string) {
				root := &cobra.Command{Use: "sepay"}
				noun := &cobra.Command{Use: name}
				verb := &cobra.Command{Use: "create"}
				root.AddCommand(noun)
				noun.AddCommand(verb)
				f.BindCommand(verb)
			}
			if tt.bound != "" {
				bind(tt.bound)
			}
			bind(tt.noun)

			_, err := f.InputOrFlag("", "prefix", "Prefix")
			require.NoError(t, err)
			_, err = f.Input("Prefix")
			require.NoError(t, err)
			assert.Equal(t, tt.want+"? Prefix\n", errOut.String(), "the banner once, before the first question")
		})
	}
}

// An ipn command sits under a product noun but mints and calls on the API host, so --api-base moves
// all of it: the banner before its first question names that host and no product host.
func TestPrompts_APIHostCommandUnderAProductNounNamesOnlyTheAPIHost(t *testing.T) {
	for _, noun := range []string{"pg", "bankhub"} {
		t.Run(noun, func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			ios, _, _, errOut := iostreams.Test()
			ios.SetStdinTTY(true)
			ios.SetStdoutTTY(true)
			ios.SetStderrTTY(true)
			f := &Factory{
				IOStreams: ios,
				Global:    &GlobalOptions{Output: "table", APIBase: "http://127.0.0.1:38799/v2"},
				Prompter:  recordingPrompter{errOut},
			}
			f.Config = config.Load
			root := &cobra.Command{Use: "sepay"}
			product := &cobra.Command{Use: noun}
			group := Annotate(&cobra.Command{Use: "ipn"}, map[string]string{AnnHost: HostAPI})
			verb := &cobra.Command{Use: "listen"}
			root.AddCommand(product)
			product.AddCommand(group)
			group.AddCommand(verb)
			f.BindCommand(verb)

			_, err := f.InputOrFlag("", "forward", "Local handler address")
			require.NoError(t, err)
			assert.Equal(t, "env: sandbox · host from --api-base\n? Local handler address\n", errOut.String())
		})
	}
}

func TestInputCheckedOrFlag_AsksAgainUntilTheAnswerPasses(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	ios.SetStderrTTY(true)
	answers := []string{"", "not a url", "https://x.test"}
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}, Prompter: &scriptedInput{answers: answers}}
	got, err := f.InputCheckedOrFlag("", "url", "Webhook URL", func(v string) error {
		if !strings.HasPrefix(v, "https://") {
			return errors.New("--url must use https")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "https://x.test", got)
	assert.Contains(t, errOut.String(), "! Webhook URL cannot be empty")
	assert.Contains(t, errOut.String(), "! --url must use https")

	_, err = promptFactory().InputCheckedOrFlag("", "url", "Webhook URL", func(string) error { return nil })
	require.EqualError(t, err, "--url is required", "unattended it stays a usage error")
}

// With stderr redirected the prompt draws on stdout, and so must what is said around it: the
// banner naming a host override, and why an answer is asked again.
func TestPrompts_BannerAndReasonsFollowThePromptWhenStderrIsRedirected(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	t.Setenv("SEPAY_SANDBOX_API_URL", "http://127.0.0.1:1")
	ios, _, out, errOut := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}, Prompter: &scriptedInput{answers: []string{"", "x.test", "https://x.test"}}}
	f.Config = config.Load
	root := &cobra.Command{Use: "sepay"}
	noun := &cobra.Command{Use: "webhook"}
	verb := &cobra.Command{Use: "create"}
	root.AddCommand(noun)
	noun.AddCommand(verb)
	f.BindCommand(verb)

	_, err := f.InputCheckedOrFlag("", "url", "Webhook URL", func(v string) error {
		if !strings.Contains(v, "://") {
			return errors.New("the URL needs a scheme")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "env: sandbox · host from SEPAY_SANDBOX_API_URL")
	assert.Contains(t, out.String(), "! Webhook URL cannot be empty")
	assert.Contains(t, out.String(), "! the URL needs a scheme", "a rejected answer's reason follows the prompt too")
	assert.Empty(t, errOut.String())
}

type scriptedInput struct {
	recordingPrompter
	answers []string
}

func (p *scriptedInput) Input(string, string) (string, error) {
	a := p.answers[0]
	p.answers = p.answers[1:]
	return a, nil
}

// enterPrompter answers every Select with Enter, keeping the cursor where it starts.
type enterPrompter struct{ recordingPrompter }

func (enterPrompter) Select(_ string, _ []string, def int) (int, error) { return def, nil }

func TestSelectDefault_MatchesTheStoredValueInAnyCase(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}, Prompter: enterPrompter{recordingPrompter{errOut}}}
	got, err := f.SelectDefault("Status", []string{"Active", "Suspended"}, "suspended")
	require.NoError(t, err)
	assert.Equal(t, "Suspended", got, "Enter keeps the stored value even when the server spells it in lower case")
}

// --no-color and NO_COLOR reach the prompt through IOStreams; the terminal draws as it is told.
func TestPrompter_DrawsInColorOnlyWhenTheStreamsAllowIt(t *testing.T) {
	for _, color := range []bool{true, false} {
		ios, _, _, _ := iostreams.Test()
		ios.SetColorEnabled(color)
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}}
		p, ok := f.prompter().(terminalPrompter)
		require.True(t, ok)
		assert.Equal(t, color, p.t.Color)
	}
}
