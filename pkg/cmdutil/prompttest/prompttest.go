// Package prompttest scripts the answers to a command's interactive prompts in tests.
package prompttest

import (
	"slices"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

// Step is one expected prompt and its answer: the option label (Select), the option labels
// (MultiSelect, as []string, the whole final set), the text typed (Input, Password), the choice
// (Confirm, as bool) or each account's answer (Accounts, as []cmdutil.FormAnswer).
type Step struct {
	Label  string
	Answer any
}

// Prompter answers prompts in the order the steps list them and fails the test on any other prompt.
type Prompter struct {
	t     testing.TB
	steps []Step
	// Defaults records what each prompt offered by default, by label: the text (Input), the
	// choice (Confirm), the option under the cursor (Select), the checked options (MultiSelect).
	Defaults map[string]any
	// Offered records the options each Select and MultiSelect showed, by label.
	Offered map[string][]string
	// Forms records the accounts each AccountsForm showed, by label.
	Forms map[string][]cmdutil.FormAccount
}

// Use makes f prompt-capable and answers its prompts from steps; every step must be used.
func Use(t testing.TB, f *cmdutil.Factory, steps ...Step) *Prompter {
	t.Helper()
	f.IOStreams.SetStdinTTY(true)
	f.IOStreams.SetStdoutTTY(true)
	f.IOStreams.SetStderrTTY(true)
	p := &Prompter{t: t, steps: steps, Defaults: map[string]any{}, Offered: map[string][]string{}, Forms: map[string][]cmdutil.FormAccount{}}
	f.Prompter = p
	t.Cleanup(func() {
		if len(p.steps) > 0 {
			t.Errorf("prompts never asked: %v", p.steps)
		}
	})
	return p
}

func (p *Prompter) next(label string) any {
	p.t.Helper()
	if len(p.steps) == 0 {
		p.t.Fatalf("unexpected prompt %q", label)
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	if step.Label != label {
		p.t.Fatalf("prompt %q asked, want %q", label, step.Label)
	}
	return step.Answer
}

func answer[T any](p *Prompter, label string) T {
	p.t.Helper()
	v, ok := p.next(label).(T)
	if !ok {
		p.t.Fatalf("prompt %q: answer is not a %T", label, v)
	}
	return v
}

func (p *Prompter) index(label, option string, options []string) int {
	p.t.Helper()
	i := slices.Index(options, option)
	if i < 0 {
		p.t.Fatalf("prompt %q has no option %q (have %v)", label, option, options)
	}
	return i
}

// Select implements cmdutil.Prompter.
func (p *Prompter) Select(label string, options []string, def int) (int, error) {
	p.t.Helper()
	p.Defaults[label] = options[def]
	p.Offered[label] = options
	return p.index(label, answer[string](p, label), options), nil
}

// MultiSelect implements cmdutil.Prompter.
func (p *Prompter) MultiSelect(label string, options []string, checked []int) ([]int, error) {
	p.t.Helper()
	defaults := []string{}
	for _, i := range checked {
		defaults = append(defaults, options[i])
	}
	p.Defaults[label] = defaults
	p.Offered[label] = options
	var picked []int
	for _, o := range answer[[]string](p, label) {
		picked = append(picked, p.index(label, o, options))
	}
	return picked, nil
}

// Input implements cmdutil.Prompter.
func (p *Prompter) Input(label, def string) (string, error) {
	p.t.Helper()
	p.Defaults[label] = def
	return answer[string](p, label), nil
}

// Password implements cmdutil.Prompter.
func (p *Prompter) Password(label string) (string, error) {
	p.t.Helper()
	return answer[string](p, label), nil
}

// Confirm implements cmdutil.Prompter.
func (p *Prompter) Confirm(label string, def bool) (bool, error) {
	p.t.Helper()
	p.Defaults[label] = def
	return answer[bool](p, label), nil
}

// Accounts implements cmdutil.Prompter.
func (p *Prompter) Accounts(label string, accounts []cmdutil.FormAccount) ([]cmdutil.FormAnswer, error) {
	p.t.Helper()
	p.Forms[label] = accounts
	return answer[[]cmdutil.FormAnswer](p, label), nil
}
