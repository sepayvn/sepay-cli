package root

import (
	"fmt"
	"strings"
	"text/template"
	"unicode"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// installUsageFunc scopes agentGuidance to this tree: cobra's AddTemplateFunc is process-global
// and would leak one tree's Factory into another's help.
func installUsageFunc(root *cobra.Command, f *cmdutil.Factory) {
	tmpl := template.Must(template.New("usage").Funcs(template.FuncMap{
		"agentGuidance":           func(c *cobra.Command) string { return agentGuidance(c, f) },
		"aliasPad":                aliasPadding,
		"rpad":                    func(s string, padding int) string { return fmt.Sprintf("%-*s", padding, s) },
		"trimTrailingWhitespaces": func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) },
	}).Parse(usageTemplate))

	root.SetUsageFunc(func(c *cobra.Command) error {
		return tmpl.Execute(c.OutOrStderr(), c)
	})
}

// aliasPadding fits "name, alias" entries; cobra's NamePadding measures Name() only.
func aliasPadding(parent *cobra.Command) int {
	var width int
	for _, c := range parent.Commands() {
		if !c.IsAvailableCommand() && c.Name() != "help" {
			continue
		}
		width = max(width, c.NamePadding(), len(c.NameAndAliases()))
	}
	return width
}

// usageTemplate is cobra's default with examples moved after the flags (as gh does) and an
// [Agent guidance] block last, so flags stay on the first screen; it drops the second "[command]" usage line,
// and lists aliases: completion offers only Name(), so help is where an alias is discoverable.
const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if and .HasAvailableSubCommands (not .Runnable)}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .NameAndAliases (aliasPad $)}} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .NameAndAliases (aliasPad $)}} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .NameAndAliases (aliasPad $)}} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{agentGuidance .}}{{if .HasHelpSubCommands}}

Help topics (sepay help <topic>):{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .Name (aliasPad $)}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// baseGuidance is the full pointer list root help carries; every subcommand gets moreGuidance instead,
// so its flags stay on the first screen.
var baseGuidance = []string{
	"Run `sepay help glossary` for what pg, bankhub, paycode, va, and quota mean.",
	"Run `sepay --map=paths` to list every command, `--map=json` for machine-readable metadata.",
	"Every read/list command supports --json. Exit codes: `sepay help exit-codes`; error codes: `sepay help errors`.",
	"Task guides, webhook and IPN bodies, and the API: https://developer.sepay.vn/en/agents/cli. Append `.md` to any page for Markdown; https://developer.sepay.vn/llms.txt indexes them.",
}

const moreGuidance = "More: sepay help glossary | exit-codes | errors; sepay --map=paths lists every command."

// envLine states the resolved environment: --env and default_env silently rebase every command. A
// production write leaves out the hint to add --env production: only the user adds it.
func envLine(f *cmdutil.Factory, prodWrite bool) string {
	if f.ActiveEnv() == config.EnvProduction {
		return "Active env: PRODUCTION (real money). Use --env sandbox for the test environment."
	}
	if prodWrite {
		return "Active env: sandbox (no real money)."
	}
	return "Active env: sandbox (no real money). Use --env production for the live environment."
}

// authLine warns when no profile is stored, since help and --map work logged out.
func authLine(f *cmdutil.Factory) string {
	if loggedIn, _ := authState(f); loggedIn {
		return ""
	}
	return "Not logged in: commands that read or write data exit 4 until you run `sepay login`."
}

func agentGuidance(cmd *cobra.Command, f *cmdutil.Factory) string {
	prodConfirm := cmdutil.ConfirmsIn(cmd, config.EnvProduction) == cmdutil.ConfirmsTerminal
	lines := []string{envLine(f, prodConfirm)}
	if auth := authLine(f); auth != "" {
		lines = append(lines, auth)
	}
	if cmd.HasParent() {
		lines = append(lines, moreGuidance)
	} else {
		lines = append(lines, baseGuidance...)
	}

	if cmdutil.Annotation(cmd, cmdutil.AnnEnvs) == "sandbox" {
		lines = append(lines, "Sandbox-only: this does not work on production.")
	}
	if scope := cmdutil.Annotation(cmd, cmdutil.AnnScope); scope != "" {
		lines = append(lines, fmt.Sprintf("On production this needs the %q scope, granted at `sepay login`.", scope))
	}
	if cmdutil.Annotation(cmd, cmdutil.AnnDestructive) == "irreversible" {
		lines = append(lines, "Irreversible: there is no command that undoes this. Confirm the target before running it.")
	}
	switch cmdutil.ConfirmsIn(cmd, config.EnvSandbox) {
	case "always":
		if prodConfirm {
			lines = append(lines, "Prompts in sandbox too; there, without a terminal it exits 2 until you pass --yes.")
		} else {
			lines = append(lines, "Prompts in sandbox too. Without a terminal it exits 2 until you pass --yes.")
		}
	case "conditional":
		lines = append(lines, "Some invocations prompt (see the note above). Without a terminal those exit 2 until you pass --yes.")
	}
	if prodConfirm {
		lines = append(lines, "On production a write runs only with --env production on the command line (default_env does not count), and --yes confirms it only when the command names the resource it changes. Otherwise it exits 2: ask the user to run it in their own terminal. Do not add --env production or --yes to a write on the user's behalf.")
	}
	// Outermost first; withoutRepeats drops a subcommand note its group's note already carries.
	var notes []string
	for c := cmd; c != nil; c = c.Parent() {
		if extra := c.Annotations[cmdutil.AnnGuidance]; extra != "" {
			notes = append([]string{extra}, notes...)
		}
	}
	lines = append(lines, withoutRepeats(notes)...)

	var b strings.Builder
	b.WriteString("\n\n[Agent guidance]")
	for _, l := range lines {
		b.WriteString("\n  ")
		b.WriteString(l)
	}
	return b.String()
}

// withoutRepeats drops a note another note already carries word for word, keeping the first of
// two equal notes: a subcommand can repeat a sentence of its group's note.
func withoutRepeats(notes []string) []string {
	kept := make([]string, 0, len(notes))
	for i, n := range notes {
		repeated := false
		for j, o := range notes {
			if strings.Contains(o, n) && (len(o) > len(n) || j < i) {
				repeated = true
				break
			}
		}
		if !repeated {
			kept = append(kept, n)
		}
	}
	return kept
}
