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

// usageTemplate is cobra's default plus an [Agent guidance] block, without the second "[command]" usage line,
// and lists aliases: completion offers only Name(), so help is where an alias is discoverable.
const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if and .HasAvailableSubCommands (not .Runnable)}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .NameAndAliases (aliasPad $)}} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .NameAndAliases (aliasPad $)}} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .NameAndAliases (aliasPad $)}} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{agentGuidance .}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

var baseGuidance = []string{
	"Run `sepay docs overview` for what SePay is, what this tool does, and which payment path to pick.",
	"Run `sepay docs glossary` for what pg, bankhub, paycode, va, and quota mean.",
	"Run `sepay docs flows` for the command order of a task, e.g. receiving a first webhook.",
	"Run `sepay --map=paths` to list every command, `--map=json` for machine-readable metadata.",
	"Every read/list command supports --json. Exit codes: `sepay docs exit-codes`.",
	"Writing a notification handler? `sepay docs webhook-payload` (bank transfer) or `sepay docs ipn-payload` (gateway order).",
	"Writing an app that calls SePay directly instead of through this CLI? `sepay docs api`.",
	"These topics cover the CLI; platform contracts are linked to developer.sepay.vn — append `.md` to any page to fetch it.",
}

// envLine states the resolved environment: --env and default_env silently rebase every command.
func envLine(f *cmdutil.Factory) string {
	if f.ActiveEnv() == config.EnvProduction {
		return "Active env: PRODUCTION — commands here hit live money. `--env sandbox` for the safe one."
	}
	return "Active env: sandbox. Add --env production to hit live money."
}

// authLine warns when no profile is stored, since help, --map and docs all work logged out.
func authLine(f *cmdutil.Factory) string {
	if loggedIn, _ := authState(f); loggedIn {
		return ""
	}
	return "Not logged in: every command that touches data exits 4 until `sepay login` runs. " +
		"Nothing here has been verified against the backend."
}

func agentGuidance(cmd *cobra.Command, f *cmdutil.Factory) string {
	lines := []string{envLine(f)}
	if auth := authLine(f); auth != "" {
		lines = append(lines, auth)
	}
	lines = append(lines, baseGuidance...)

	if cmdutil.Annotation(cmd, cmdutil.AnnEnvs) == "sandbox" {
		lines = append(lines, "Sandbox-only: this does not work on production.")
	}
	if scope := cmdutil.Annotation(cmd, cmdutil.AnnScope); scope != "" {
		lines = append(lines, fmt.Sprintf("On production this needs the %q scope: sepay auth refresh --scopes %s", scope, scope))
	}
	if cmdutil.Annotation(cmd, cmdutil.AnnDestructive) == "irreversible" {
		lines = append(lines, "Irreversible: there is no command that undoes this. Confirm the target before running it.")
	}
	switch cmdutil.Annotation(cmd, cmdutil.AnnConfirms) {
	case "always":
		lines = append(lines, "Prompts in sandbox too. Without a terminal it exits 2 until you pass --yes.")
	case "conditional":
		lines = append(lines, "Some invocations prompt (see the note above). Without a terminal those exit 2 until you pass --yes.")
	}
	// Outermost first: a group's note and its subcommand's are different facts.
	var notes []string
	for c := cmd; c != nil; c = c.Parent() {
		if extra := c.Annotations[cmdutil.AnnGuidance]; extra != "" {
			notes = append([]string{extra}, notes...)
		}
	}
	lines = append(lines, notes...)

	var b strings.Builder
	b.WriteString("\n\n[Agent guidance]")
	for _, l := range lines {
		b.WriteString("\n  ")
		b.WriteString(l)
	}
	return b.String()
}
