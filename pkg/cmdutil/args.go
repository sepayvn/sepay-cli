package cmdutil

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// switchValues are words typed after a bool flag; pflag bools never consume the next token.
var switchValues = map[string]bool{
	"on": true, "off": true, "true": true, "false": true,
	"yes": true, "no": true, "1": true, "0": true,
}

// NoArgs is cobra.NoArgs but reports a stray "on"/"off" as bool-flag syntax.
func NoArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if err := switchSyntaxError(cmd, args, anySwitch); err != nil {
		return err
	}
	return cobra.NoArgs(cmd, args)
}

// MaximumNArgs is cobra.MaximumNArgs with the same switch-word reading, limited to the command's own
// flags because globals like --yes ride on every call.
func MaximumNArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= n {
			return switchSyntaxError(cmd, args, ownSwitches)
		}
		if err := switchSyntaxError(cmd, args, anySwitch); err != nil {
			return err
		}
		return cobra.MaximumNArgs(n)(cmd, args)
	}
}

func ownSwitches(cmd *cobra.Command, f *pflag.Flag) bool {
	return cmd.InheritedFlags().Lookup(f.Name) == nil
}

func anySwitch(*cobra.Command, *pflag.Flag) bool { return true }

// switchSyntaxError returns nil when the surplus does not look like a bool-flag mistake.
func switchSyntaxError(cmd *cobra.Command, args []string, consider func(*cobra.Command, *pflag.Flag) bool) error {
	value := ""
	for _, a := range args {
		if switchValues[strings.ToLower(a)] {
			value = a
			break
		}
	}
	if value == "" {
		return nil
	}

	var switches []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Value.Type() == "bool" && f.NoOptDefVal != "" && consider(cmd, f) {
			switches = append(switches, f.Name)
		}
	})
	escape := ""
	if name := Annotation(cmd, AnnValueFlag); name != "" {
		escape = fmt.Sprintf("; if %q is the value you mean, pass --%s %s", value, name, value)
	}
	switch len(switches) {
	case 0:
		return nil
	case 1:
		return &FlagError{Err: fmt.Errorf(
			"--%s is a switch, not a value: use --%s or --%s=false (got the extra argument %q)%s",
			switches[0], switches[0], switches[0], value, escape)}
	default:
		return &FlagError{Err: fmt.Errorf(
			"a switch takes no separate value: write --flag=false, not --flag false (got the extra argument %q)%s",
			value, escape)}
	}
}

// FirstArg returns args[0], or "".
func FirstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
