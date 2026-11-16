package cmdutil

import (
	"strings"

	"github.com/spf13/cobra"
)

// Annotation keys read by help and `sepay --map`.
const (
	AnnImplemented = "sepay:implemented" // "no" when the command has no backend yet
	AnnEnvs        = "sepay:envs"        // "sandbox" when production is refused, else unset (both)
	// AnnDestructive marks a command whose effect no other command can undo.
	AnnDestructive = "sepay:destructive" // "irreversible", or unset
	// AnnConfirms states when a command prompts: one value for every env it runs in, or
	// comma-separated env=value pairs, e.g. "sandbox=always,production=terminal". Values are
	// "always" | "conditional" | ConfirmsTerminal; an env left out has none. Read it with ConfirmsIn.
	AnnConfirms = "sepay:confirms"
	AnnScope    = "sepay:scope"    // capability scope required on the production host
	AnnGuidance = "sepay:guidance" // extra [Agent guidance] line for this command
	// AnnRequired lists flags a non-interactive run needs; written only by MarkRequired.
	AnnRequired = "sepay:required"
	// AnnValueFlag names a flag taking the positional's value, for values that look like switch words.
	AnnValueFlag = "sepay:value-flag"
	// AnnHost names the hosts a command under a product noun calls when they are not its noun's.
	AnnHost = "sepay:host" // HostAPI | HostPGCheckout, or unset
)

// ConfirmsTerminal is the AnnConfirms value for a production write: it runs only with --env
// production on the command line, and --yes confirms it only when it names one resource.
const ConfirmsTerminal = "terminal"

// ConfirmsIn returns cmd's AnnConfirms value for env, inherited like Annotation.
func ConfirmsIn(cmd *cobra.Command, env string) string {
	return ConfirmsValue(Annotation(cmd, AnnConfirms), env)
}

// ConfirmsValue resolves a raw AnnConfirms value for env.
func ConfirmsValue(raw, env string) string {
	if !strings.Contains(raw, "=") {
		return strings.TrimSpace(raw)
	}
	for _, pair := range strings.Split(raw, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) == env {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// AnnHost values.
const (
	HostAPI        = "api"         // the SePay API host only, e.g. the ipn commands
	HostPGCheckout = "pg-checkout" // the PG API host and the hosted-checkout host
)

// MarkRequired marks flags required when the command cannot prompt, in help and `--map=json`.
// Only for unconditionally required flags.
func MarkRequired(cmd *cobra.Command, names ...string) *cobra.Command {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		fl := cmd.Flags().Lookup(name)
		if fl == nil {
			continue // a rename that missed this call must not fake the contract
		}
		kept = append(kept, name)
		if !strings.Contains(fl.Usage, "(required") {
			fl.Usage = strings.TrimRight(fl.Usage, " ") + " (required)"
		}
	}
	if len(kept) == 0 {
		return cmd
	}
	return Annotate(cmd, map[string]string{AnnRequired: strings.Join(kept, ",")})
}

// RequiredFlags returns the flags MarkRequired declared on cmd, as a set.
func RequiredFlags(cmd *cobra.Command) map[string]bool {
	raw := cmd.Annotations[AnnRequired]
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}

// Annotate merges kv into cmd's annotations and returns cmd.
func Annotate(cmd *cobra.Command, kv map[string]string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string, len(kv))
	}
	for k, v := range kv {
		cmd.Annotations[k] = v
	}
	return cmd
}

// Annotation returns cmd's value for key, falling back to the nearest ancestor.
func Annotation(cmd *cobra.Command, key string) string {
	for c := cmd; c != nil; c = c.Parent() {
		if v := c.Annotations[key]; v != "" {
			return v
		}
	}
	return ""
}
