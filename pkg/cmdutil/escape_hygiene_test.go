package cmdutil

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

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

func TestLabelsOf_SanitizesServerLabels(t *testing.T) {
	labels := labelsOf([]PromptChoice{{Label: "NGUYEN VAN A" + escapePayload, Value: "ba_1"}})
	assertClean(t, "labelsOf", labels[0])
	require.Contains(t, labels[0], "NGUYEN VAN A")
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

// capturePrompter records what each prompt was asked to draw.
type capturePrompter struct {
	drawn []string
}

func (p *capturePrompter) Input(label, def string) (string, error) {
	p.drawn = append(p.drawn, label, def)
	return "", nil
}

func (p *capturePrompter) Password(label string) (string, error) {
	p.drawn = append(p.drawn, label)
	return "", nil
}

func (p *capturePrompter) Confirm(label string, _ bool) (bool, error) {
	p.drawn = append(p.drawn, label)
	return true, nil
}

func (p *capturePrompter) Select(label string, options []string, _ int) (int, error) {
	p.drawn = append(append(p.drawn, label), options...)
	return 0, nil
}

func (p *capturePrompter) MultiSelect(label string, options []string, _ []int) ([]int, error) {
	p.drawn = append(append(p.drawn, label), options...)
	return []int{0}, nil
}

func (p *capturePrompter) Accounts(label string, accounts []FormAccount) ([]FormAnswer, error) {
	p.drawn = append(p.drawn, label)
	for _, a := range accounts {
		p.drawn = append(p.drawn, a.Title, a.Note)
		for _, m := range a.Modes {
			p.drawn = append(p.drawn, m.Label, m.Short)
		}
		for _, v := range a.VAs {
			p.drawn = append(p.drawn, v.Label, v.Short)
		}
	}
	return make([]FormAnswer, len(accounts)), nil
}

func TestPrompts_CleanEveryServerStringBeforeDrawing(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	p := &capturePrompter{}
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}, Prompter: p}

	got, err := f.InputWithDefault("Name"+escapePayload, "stored"+escapePayload)
	require.NoError(t, err)
	require.Equal(t, "stored"+escapePayload, got, "Enter returns the caller's raw default")
	_, err = f.Password("Secret" + escapePayload)
	require.NoError(t, err)
	_, err = f.AskYesNo("Delete"+escapePayload, false)
	require.NoError(t, err)
	_, err = f.AccountsForm("Form"+escapePayload, []FormAccount{{
		Title: "MSB" + escapePayload, Note: "note" + escapePayload,
		Modes: []FormMode{{Label: "All" + escapePayload, Short: "all" + escapePayload}},
		VAs:   []FormOption{{Label: "VA" + escapePayload, Short: "va" + escapePayload}},
	}})
	require.NoError(t, err)

	require.NotEmpty(t, p.drawn)
	for _, s := range p.drawn {
		assertClean(t, "prompt text", s)
	}
}

// Picker options are usually API data (account names, labels); each picker draws them cleaned.
func TestPickers_CleanEveryServerStringBeforeDrawing(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	ios.SetStderrTTY(true)
	choices := []PromptChoice{{Label: "A" + escapePayload, Value: "a"}, {Label: "B" + escapePayload, Value: "b"}}
	pickers := map[string]func(f *Factory) error{
		"SelectChoiceOrFlag": func(f *Factory) error {
			_, err := f.SelectChoiceOrFlag("", "x", "Pick"+escapePayload, choices)
			return err
		},
		"MultiSelectChoicesOrFlag": func(f *Factory) error {
			_, err := f.MultiSelectChoicesOrFlag(nil, "x", "Pick"+escapePayload, choices)
			return err
		},
		"SelectOrFlag": func(f *Factory) error {
			_, err := f.SelectOrFlag("", "x", "Pick"+escapePayload, []string{"in" + escapePayload, "out"})
			return err
		},
		"SelectChoiceDefault": func(f *Factory) error {
			_, err := f.SelectChoiceDefault("Pick"+escapePayload, choices, "b")
			return err
		},
		"MultiSelectChoicesDefault": func(f *Factory) error {
			_, err := f.MultiSelectChoicesDefault("Pick"+escapePayload, choices, []string{"a"})
			return err
		},
	}
	for name, pick := range pickers {
		t.Run(name, func(t *testing.T) {
			p := &capturePrompter{}
			require.NoError(t, pick(&Factory{IOStreams: ios, Global: &GlobalOptions{}, Prompter: p}))
			require.NotEmpty(t, p.drawn)
			for _, s := range p.drawn {
				assertClean(t, name, s)
			}
		})
	}
}
