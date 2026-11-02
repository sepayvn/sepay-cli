package cmdutil

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/core"
	"github.com/AlecAivazis/survey/v2/terminal"
)

// canPrompt also requires --no-input unset, so a PTY caller can turn a missing flag into a usage error.
func (f *Factory) canPrompt() bool {
	return !f.Global.NoInput && f.IOStreams.CanPrompt() &&
		f.Global.Output != "json" && f.Global.Output != "yaml" && !f.Global.Quiet
}

// CanPrompt reports whether an interactive prompt is possible.
func (f *Factory) CanPrompt() bool { return f.canPrompt() }

// PromptChoice is one option: Label is shown, Value is returned.
type PromptChoice struct {
	Label string
	Value string
}

// keepOneMatch is survey's filter, but an empty match shows every option: both pickers index
// options[0] or options[-1] on an empty list (select.go:313, multiselect.go:97-117).
func keepOneMatch(options []string) survey.AskOpt {
	return survey.WithFilter(func(filter, value string, index int) bool {
		for _, opt := range options {
			if filterMatches(filter, opt) {
				return filterMatches(filter, value)
			}
		}
		return true
	})
}

// filterMatches copies survey's default predicate (survey.go:53).
func filterMatches(filter, value string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(filter))
}

// ask runs a survey prompt; survey catches ^C itself in raw mode, so the signal context never fires.
func ask(p survey.Prompt, response any, opts ...survey.AskOpt) error {
	err := survey.AskOne(p, response, opts...)
	if errors.Is(err, terminal.InterruptErr) {
		return ErrInterrupted
	}
	return err
}

func askPicker(prompt survey.Prompt, response any, options []string) error {
	return ask(prompt, response, keepOneMatch(options))
}

// SelectChoiceOrFlag returns val, else prompts for one choice, else a usage error naming flag.
func (f *Factory) SelectChoiceOrFlag(val, flag, label string, choices []PromptChoice) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.canPrompt() || len(choices) == 0 {
		return "", &FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	// Bind the index: two choices can share a label.
	var answer core.OptionAnswer
	labels := labelsOf(choices)
	if err := askPicker(&survey.Select{Message: label, Options: labels}, &answer, labels); err != nil {
		return "", err
	}
	return choices[answer.Index].Value, nil
}

// MultiSelectChoicesOrFlag returns vals, else prompts for choices, else a usage error naming flag.
func (f *Factory) MultiSelectChoicesOrFlag(vals []string, flag, label string, choices []PromptChoice) ([]string, error) {
	if len(vals) > 0 {
		return vals, nil
	}
	if !f.canPrompt() || len(choices) == 0 {
		return nil, &FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	var answers []core.OptionAnswer
	labels := labelsOf(choices)
	if err := askPicker(&survey.MultiSelect{Message: label, Options: labels}, &answers, labels); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(answers))
	for _, a := range answers {
		out = append(out, choices[a.Index].Value)
	}
	return out, nil
}

// Input prompts for optional free text. Call only after CanPrompt.
func (f *Factory) Input(label string) (string, error) {
	var answer string
	if err := ask(&survey.Input{Message: label}, &answer); err != nil {
		return "", err
	}
	return answer, nil
}

// InputWithDefault prompts for free text, returning def on Enter. Call only after CanPrompt.
func (f *Factory) InputWithDefault(label, def string) (string, error) {
	var answer string
	if err := ask(&survey.Input{Message: label, Default: def}, &answer); err != nil {
		return "", err
	}
	return answer, nil
}

// Password prompts for a secret without echoing it. Call only after CanPrompt.
func (f *Factory) Password(label string) (string, error) {
	var answer string
	if err := ask(&survey.Password{Message: label}, &answer); err != nil {
		return "", err
	}
	return answer, nil
}

// labelsOf sanitises server-supplied labels: survey interpolates them verbatim, and an escape or
// newline corrupts the redraw.
func labelsOf(choices []PromptChoice) []string {
	labels := make([]string, len(choices))
	for i, c := range choices {
		labels[i] = jsonfmt.Clean(c.Label)
	}
	return labels
}

// InputOrFlag returns val, else prompts for free text, else a usage error naming flag.
func (f *Factory) InputOrFlag(val, flag, label string) (string, error) {
	return f.InputOrArg(val, "--"+flag, label)
}

// InputOrArg is InputOrFlag for a positional argument; noun names it in the error.
func (f *Factory) InputOrArg(val, noun, label string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.canPrompt() {
		return "", &FlagError{Err: fmt.Errorf("%s is required", noun)}
	}
	return f.Input(label)
}

// maxSecretBytes caps a piped secret.
const maxSecretBytes = 64 << 10

// SecretFromFlag reads the secret from stdin when val is "-", keeping it out of argv and shell
// history. An inline value still works but warns.
func (f *Factory) SecretFromFlag(val, flag string) (string, error) {
	if val != "-" {
		// "" is not a secret: for --api-key it turns auth off.
		if val != "" {
			f.ErrPrintf("! --%s passed inline is visible in shell history and in ps; pass - to read it from stdin\n", flag)
		}
		return val, nil
	}
	if f.IOStreams.IsStdinTTY() {
		return "", &FlagError{Err: fmt.Errorf("--%s is \"-\" but stdin is a terminal; pipe the secret in, or drop --%s to be prompted for it", flag, flag)}
	}
	// One pipe carries one secret; name the flag that already drained it.
	if f.stdinSecretFlag != "" {
		return "", &FlagError{Err: fmt.Errorf(
			"--%s cannot also read stdin; --%s already did, so pass one of them inline or leave it out to be prompted", flag, f.stdinSecretFlag)}
	}
	data, err := io.ReadAll(io.LimitReader(f.IOStreams.In, maxSecretBytes))
	if err != nil {
		return "", err
	}
	f.stdinSecretFlag = flag
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", &FlagError{Err: fmt.Errorf("--%s is \"-\" but stdin carried no secret", flag)}
	}
	return secret, nil
}

// SelectOrFlag returns val, else prompts for one of options, else a usage error listing them.
func (f *Factory) SelectOrFlag(val, flag, label string, options []string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.canPrompt() {
		return "", &FlagError{Err: fmt.Errorf("--%s is required (one of: %s)", flag, strings.Join(options, ", "))}
	}
	var answer string
	if err := askPicker(&survey.Select{Message: label, Options: options}, &answer, options); err != nil {
		return "", err
	}
	return answer, nil
}

// Confirm returns true under --yes, else prompts; unattended without --yes is a usage error.
func (f *Factory) Confirm(label string) (bool, error) {
	if f.Global.Yes {
		return true, nil
	}
	if !f.canPrompt() {
		return false, &FlagError{Err: fmt.Errorf("%s requires confirmation; pass --yes", label)}
	}
	ok := false
	if err := ask(&survey.Confirm{Message: label}, &ok); err != nil {
		return false, err
	}
	return ok, nil
}

// AskYesNo prompts for yes/no, returning def on Enter; --yes does not answer it. Call only after CanPrompt.
func (f *Factory) AskYesNo(label string, def bool) (bool, error) {
	answer := def
	if err := ask(&survey.Confirm{Message: label, Default: def}, &answer); err != nil {
		return false, err
	}
	return answer, nil
}
