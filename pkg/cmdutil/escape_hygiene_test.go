package cmdutil

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/core"
	"github.com/stretchr/testify/require"
)

// escapePayload clears the screen, homes the cursor, retitles the window and rings the bell.
const escapePayload = "\x1b[2J\x1b[H\x1b]0;pwn\x07"

func assertClean(t *testing.T, where, got string) {
	t.Helper()
	for _, seq := range []string{"\x1b[2J", "\x1b[H", "\x1b]0;", "\x07", "\n\r"} {
		if strings.Contains(got, seq) {
			t.Errorf("%s leaked %q: %q", where, seq, got)
		}
	}
}

func TestConfirmMutation_BannerIsSanitized(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: config.EnvProduction, Yes: true}}
	require.NoError(t, f.ConfirmMutation("api DELETE /v2/bank-accounts/ba_x/va/0123456789"+escapePayload, true))
	assertClean(t, "production banner", errOut.String())
	require.Contains(t, errOut.String(), "/v2/bank-accounts/ba_x/va/0123456789", "the operation must stay readable")
}

func TestConfirmMutation_CancelErrorIsSanitized(t *testing.T) {
	ios, _, _, _ := iostreams.Test() // non-TTY, no --yes: refuses with a usage error
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: config.EnvProduction}}
	err := f.ConfirmMutation("cancel order INV-1"+escapePayload, true)
	require.Error(t, err)
	assertClean(t, "confirm error", err.Error())
}

func TestConfirmMutation_SandboxConfirmIsSanitized(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: config.EnvSandbox}}
	err := f.ConfirmMutation("revoke virtual account 0123456789"+escapePayload, true)
	require.Error(t, err)
	assertClean(t, "sandbox confirm error", err.Error())
}

func TestLabelsOf_SanitizesLabelsNotValues(t *testing.T) {
	got := labelsOf([]PromptChoice{
		{Label: "NGUYEN VAN A" + escapePayload, Value: "ba_1" + escapePayload},
	})
	assertClean(t, "labelsOf", got[0])
	require.Contains(t, got[0], "NGUYEN VAN A")
}

func TestLabelsOf_DropsEmbeddedNewline(t *testing.T) {
	got := labelsOf([]PromptChoice{{Label: "row one\nrow two", Value: "ba_1"}})
	require.NotContains(t, got[0], "\n")
}

func TestLabelsOf_SurveyRendersSanitizedLabel(t *testing.T) {
	opts := labelsOf([]PromptChoice{{Label: "NGUYEN VAN A" + escapePayload, Value: "ba_1"}})
	data := survey.SelectTemplateData{
		Select:      survey.Select{Message: "Bank account", Options: opts},
		PageEntries: core.OptionAnswerList(opts),
		Config:      &survey.PromptConfig{},
	}
	rendered, _, err := core.RunTemplate(survey.SelectQuestionTemplate, data)
	require.NoError(t, err)
	for _, seq := range []string{"\x1b[2J", "\x1b]0;", "\x07"} {
		if strings.Contains(rendered, seq) {
			t.Errorf("survey rendered %q: %q", seq, rendered)
		}
	}
}

func TestPrintf_SanitizesArgsAndKeepsColor(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	ios.SetColorEnabled(true)
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	f.Printf("%s Ref: %s (%d)\n", f.SuccessIcon(), "084e179d"+escapePayload, 7)
	assertClean(t, "Printf", out.String())
	require.Contains(t, out.String(), "084e179d")
	require.Contains(t, out.String(), "\x1b[32m", "the CLI's own color must survive")
	require.Contains(t, out.String(), "(7)")

	f.ErrPrintf("poll: %s\n", "trk_1"+escapePayload)
	assertClean(t, "ErrPrintf", errOut.String())
	require.Contains(t, errOut.String(), "trk_1")
}

func TestHandleError_SanitizesHumanLineKeepingNewlines(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	err := &FlagError{Err: fmt.Errorf("path %q is odd; use %s", "/x", "/webhooks/wh_1"+escapePayload)}
	require.Equal(t, ExitUsage, HandleError(f, err))
	assertClean(t, "HandleError line", errOut.String())
	require.Contains(t, errOut.String(), "/webhooks/wh_1")

	errOut.Reset()
	multi := errors.New("validation failed" + escapePayload + "\n  - url: must be https" + escapePayload)
	HandleError(f, multi)
	assertClean(t, "HandleError multi-line", errOut.String())
	require.Contains(t, errOut.String(), "\n  - url: must be https", "line structure must survive")
}
