package cmdutil

import (
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/AlecAivazis/survey/v2"
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
	assert.Equal(t, "", got)
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

func filterOf(t *testing.T, opt survey.AskOpt) func(string, string, int) bool {
	t.Helper()
	var opts survey.AskOptions
	require.NoError(t, opt(&opts))
	return opts.PromptConfig.Filter
}

func keptBy(t *testing.T, options []string, filter string) []string {
	t.Helper()
	keep := filterOf(t, keepOneMatch(options))
	var out []string
	for i, o := range options {
		if keep(filter, o, i) {
			out = append(out, o)
		}
	}
	return out
}

var pickerOptions = []string{"BIDV 970418", "ACB 970416", "VPBank 970432"}

func TestKeepOneMatch_NoMatchKeepsEveryOption(t *testing.T) {
	assert.Equal(t, pickerOptions, keptBy(t, pickerOptions, "zzzzzzzz"))
}

func TestKeepOneMatch_AMatchStillFilters(t *testing.T) {
	assert.Equal(t, []string{"ACB 970416"}, keptBy(t, pickerOptions, "acb"))
	assert.Equal(t, []string{"BIDV 970418", "ACB 970416"}, keptBy(t, pickerOptions, "97041"))
}

func TestKeepOneMatch_MatchesCaseInsensitively(t *testing.T) {
	assert.Equal(t, []string{"VPBank 970432"}, keptBy(t, pickerOptions, "vpBANK"))
}

func TestKeepOneMatch_EmptyFilterKeepsEveryOption(t *testing.T) {
	assert.Equal(t, pickerOptions, keptBy(t, pickerOptions, ""))
}

func TestKeepOneMatch_NeverYieldsAnEmptyList(t *testing.T) {
	for _, filter := range []string{"", " ", "zzzzzzzz", "acb", "97041", "vpBANK", "!@#$%", "ACB 970416", "97041 "} {
		t.Run(filter, func(t *testing.T) {
			assert.NotEmpty(t, keptBy(t, pickerOptions, filter))
		})
	}
}

func TestKeepOneMatch_HoldsForASingleOption(t *testing.T) {
	one := []string{"ACB 970416"}
	assert.Equal(t, one, keptBy(t, one, "zzzzzzzz"))
	assert.Equal(t, one, keptBy(t, one, "acb"))
}
