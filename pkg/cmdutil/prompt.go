package cmdutil

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/prompt"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
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

// Prompter asks the interactive questions once canPrompt allows them. Labels and options arrive
// sanitised; Select and MultiSelect answer with indexes, Input with the text typed ("" keeps def).
type Prompter interface {
	Select(label string, options []string, def int) (int, error)
	MultiSelect(label string, options []string, picked []int) ([]int, error)
	Input(label, def string) (string, error)
	Password(label string) (string, error)
	Confirm(label string, def bool) (bool, error)
	Accounts(label string, accounts []FormAccount) ([]FormAnswer, error)
}

type (
	// FormAccount is one bank account in AccountsForm.
	FormAccount = prompt.Account
	// FormMode is one virtual account mode a FormAccount offers.
	FormMode = prompt.Mode
	// FormOption is one virtual account a FormAccount can check.
	FormOption = prompt.Option
	// FormAnswer is what one FormAccount was set to.
	FormAnswer = prompt.AccountAnswer
)

// prompter is Factory.Prompter, else the terminal, after the env banner.
func (f *Factory) prompter() Prompter {
	f.bannerBeforePrompt()
	if f.Prompter != nil {
		return f.Prompter
	}
	return terminalPrompter{prompt.Terminal{In: f.IOStreams.In, Out: f.promptOut(), Color: f.IOStreams.ColorEnabled()}}
}

// promptOut is where prompts and the lines that go with them draw: stderr, so stdout carries only
// data, or stdout when stderr is redirected, which canPrompt has already found to be a terminal.
func (f *Factory) promptOut() io.Writer {
	if f.IOStreams.IsStderrTTY() {
		return f.IOStreams.ErrOut
	}
	return f.IOStreams.Out
}

type terminalPrompter struct{ t prompt.Terminal }

func (p terminalPrompter) Select(label string, options []string, def int) (int, error) {
	return prompt.Select(p.t, label, options, def)
}

func (p terminalPrompter) MultiSelect(label string, options []string, picked []int) ([]int, error) {
	return prompt.MultiSelect(p.t, label, options, picked)
}

func (p terminalPrompter) Input(label, def string) (string, error) {
	return prompt.Input(p.t, label, def)
}

func (p terminalPrompter) Password(label string) (string, error) {
	return prompt.Password(p.t, label)
}

func (p terminalPrompter) Confirm(label string, def bool) (bool, error) {
	return prompt.Confirm(p.t, label, def)
}

func (p terminalPrompter) Accounts(label string, accounts []FormAccount) ([]FormAnswer, error) {
	return prompt.Accounts(p.t, label, accounts)
}

// asked maps the prompt's interrupt to ErrInterrupted: the prompt reads ^C as a key in raw mode,
// so the signal context never fires.
func asked[T any](answer T, err error) (T, error) {
	if errors.Is(err, prompt.ErrInterrupted) {
		return answer, ErrInterrupted
	}
	return answer, err
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
	i, err := asked(f.prompter().Select(jsonfmt.Clean(label), labelsOf(choices), 0))
	if err != nil {
		return "", err
	}
	return choices[i].Value, nil
}

// MultiSelectChoicesOrFlag returns vals, else prompts for choices, else a usage error naming flag.
func (f *Factory) MultiSelectChoicesOrFlag(vals []string, flag, label string, choices []PromptChoice) ([]string, error) {
	if len(vals) > 0 {
		return vals, nil
	}
	if !f.canPrompt() || len(choices) == 0 {
		return nil, &FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	picked, err := asked(f.prompter().MultiSelect(jsonfmt.Clean(label), labelsOf(choices), nil))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(picked))
	for _, i := range picked {
		out = append(out, choices[i].Value)
	}
	return out, nil
}

// Input prompts for optional free text. Call only after CanPrompt.
func (f *Factory) Input(label string) (string, error) {
	return f.InputWithDefault(label, "")
}

// InputWithDefault prompts for free text, returning def on Enter. Call only after CanPrompt.
func (f *Factory) InputWithDefault(label, def string) (string, error) {
	answer, err := asked(f.prompter().Input(jsonfmt.Clean(label), jsonfmt.Clean(def)))
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// Password prompts for a secret without echoing it. Call only after CanPrompt.
func (f *Factory) Password(label string) (string, error) {
	return asked(f.prompter().Password(jsonfmt.Clean(label)))
}

// labelsOf sanitises server-supplied labels: the prompt draws them verbatim, and an escape or
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
	shown := make([]string, len(options))
	for i, o := range options {
		shown[i] = jsonfmt.Clean(o)
	}
	i, err := asked(f.prompter().Select(jsonfmt.Clean(label), shown, 0))
	if err != nil {
		return "", err
	}
	return options[i], nil
}

// Confirm returns true under --yes, else prompts; unattended without --yes is a usage error.
func (f *Factory) Confirm(label string) (bool, error) {
	if f.Global.Yes {
		return true, nil
	}
	if !f.canPrompt() {
		return false, &FlagError{Err: fmt.Errorf("%s requires confirmation; pass --yes", label)}
	}
	return f.AskYesNo(label, false)
}

// AskYesNo prompts for yes/no, returning def on Enter; --yes does not answer it. Call only after CanPrompt.
func (f *Factory) AskYesNo(label string, def bool) (bool, error) {
	return asked(f.prompter().Confirm(jsonfmt.Clean(label), def))
}

// SelectChoiceDefault prompts for one choice with the cursor on the one whose Value is def. Call
// only after CanPrompt.
func (f *Factory) SelectChoiceDefault(label string, choices []PromptChoice, def string) (string, error) {
	start := 0
	for i, c := range choices {
		if c.Value == def {
			start = i
		}
	}
	i, err := asked(f.prompter().Select(jsonfmt.Clean(label), labelsOf(choices), start))
	if err != nil {
		return "", err
	}
	return choices[i].Value, nil
}

// MultiSelectChoicesDefault prompts for any number of choices, those whose Value is in picked
// already checked. Call only after CanPrompt.
func (f *Factory) MultiSelectChoicesDefault(label string, choices []PromptChoice, picked []string) ([]string, error) {
	var start []int
	for i, c := range choices {
		if slices.Contains(picked, c.Value) {
			start = append(start, i)
		}
	}
	got, err := asked(f.prompter().MultiSelect(jsonfmt.Clean(label), labelsOf(choices), start))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(got))
	for _, i := range got {
		out = append(out, choices[i].Value)
	}
	return out, nil
}

// AccountsForm shows one form editing what each account notifies; server text in it is sanitised
// first. Call only after CanPrompt.
func (f *Factory) AccountsForm(label string, accounts []FormAccount) ([]FormAnswer, error) {
	clean := make([]FormAccount, len(accounts))
	for i, a := range accounts {
		a.Title, a.Note = jsonfmt.Clean(a.Title), jsonfmt.Clean(a.Note)
		a.Modes = slices.Clone(a.Modes)
		for j := range a.Modes {
			a.Modes[j].Label, a.Modes[j].Short = jsonfmt.Clean(a.Modes[j].Label), jsonfmt.Clean(a.Modes[j].Short)
		}
		a.VAs = slices.Clone(a.VAs)
		for j := range a.VAs {
			a.VAs[j] = FormOption{Label: jsonfmt.Clean(a.VAs[j].Label), Short: jsonfmt.Clean(a.VAs[j].Short)}
		}
		clean[i] = a
	}
	return asked(f.prompter().Accounts(jsonfmt.Clean(label), clean))
}

// InputCheckedOrFlag is InputOrFlag for a value check accepts: on a terminal a rejected answer,
// empty included, is reported and asked again, so a bad value never reaches the request.
func (f *Factory) InputCheckedOrFlag(val, flag, label string, check func(string) error) (string, error) {
	if val != "" || !f.canPrompt() {
		return f.InputOrFlag(val, flag, label)
	}
	for {
		answer, err := f.Input(label)
		if err != nil {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			Fprintf(f.promptOut(), "! %s cannot be empty\n", label)
			continue
		}
		if err := check(answer); err != nil {
			Fprintf(f.promptOut(), "! %s\n", err.Error())
			continue
		}
		return answer, nil
	}
}

// SelectDefault prompts for one of options with the cursor on def, matched case-insensitively, so
// an edit that only presses Enter keeps the stored value. Call only after CanPrompt.
func (f *Factory) SelectDefault(label string, options []string, def string) (string, error) {
	choices := make([]PromptChoice, len(options))
	start := ""
	for i, o := range options {
		choices[i] = PromptChoice{Label: o, Value: o}
		if strings.EqualFold(o, def) {
			start = o
		}
	}
	return f.SelectChoiceDefault(label, choices, start)
}
