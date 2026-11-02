package cmdutil

import (
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A value list counts only at the start of the help or after "label: ", and never inside
// parentheses, where the help names another flag's values ("required for --bank-mode single|multi").
var (
	parenthetical = regexp.MustCompile(`\([^)]*\)`)
	enumInUsage   = regexp.MustCompile(`(?:^|:\s)([\w-]+(?:\|[\w-]+)+)(?:$|[\s;,])`)
)

func flagEnum(usage string) []string {
	m := enumInUsage.FindStringSubmatch(parenthetical.ReplaceAllString(usage, ""))
	if m == nil {
		return nil
	}
	return strings.Split(m[1], "|")
}

// CompleteEnumFlags makes every flag whose help lists its values complete to exactly those
// values, so help and completion cannot drift. A flag with its own completion keeps it.
func CompleteEnumFlags(root *cobra.Command) {
	root.LocalFlags().VisitAll(func(fl *pflag.Flag) {
		if _, ok := root.GetFlagCompletionFunc(fl.Name); ok {
			return
		}
		if values := flagEnum(fl.Usage); values != nil {
			_ = root.RegisterFlagCompletionFunc(fl.Name, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
		}
	})
	for _, c := range root.Commands() {
		CompleteEnumFlags(c)
	}
}

// CompleteProfiles completes stored profile names.
func CompleteProfiles(f *Factory) cobra.CompletionFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		mgr, err := f.Credentials()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, 0, len(mgr.Hosts().Profiles))
		for name := range mgr.Hosts().Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// CompleteFirstArg completes the first positional argument with fn and offers nothing after it.
func CompleteFirstArg(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fn(cmd, args, toComplete)
	}
}
