package root

import (
	"embed"
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

//go:embed topics/*.md
var topicFiles embed.FS

// helpTopics ship inside the binary, so `sepay help <topic>` always matches the version running.
var helpTopics = []struct{ name, short string }{
	{"environment", "Environment variables and config files sepay reads"},
	{"errors", "The error_code values and what to do about each"},
	{"exit-codes", "Exit codes used by sepay"},
	{"glossary", "What pg, bankhub, paycode, va, quota and xid mean"},
}

// newHelpTopic is a command with no Run, which cobra lists under "Additional help topics";
// both `sepay help <topic>` and `sepay <topic>` print its Markdown.
func newHelpTopic(f *cmdutil.Factory, name, short string) *cobra.Command {
	body, err := topicFiles.ReadFile("topics/" + name + ".md")
	if err != nil {
		panic(err)
	}
	cmd := &cobra.Command{Use: name, Short: short, Long: string(body)}
	cmd.SetHelpFunc(func(*cobra.Command, []string) {
		_, _ = f.IOStreams.Out.Write(body)
	})
	return cmd
}

// newHelpCmd replaces cobra's help command, which prints root help with exit 0 for a name it
// cannot find: a script asking for a topic that does not exist must fail.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command | topic]",
		Short: "Help about any command or topic",
		ValidArgsFunction: func(c *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			parent := c.Root()
			if len(args) > 0 {
				found, rest, err := parent.Find(args)
				if err != nil || len(rest) > 0 {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				parent = found
			}
			var names []string
			for _, sub := range parent.Commands() {
				if sub.IsAvailableCommand() || sub.IsAdditionalHelpTopicCommand() {
					names = append(names, sub.Name()+"\t"+sub.Short)
				}
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(c *cobra.Command, args []string) error {
			target, rest, err := c.Root().Find(args)
			if err != nil || len(rest) > 0 {
				topics := make([]string, len(helpTopics))
				for i, t := range helpTopics {
					topics[i] = t.name
				}
				return &cmdutil.FlagError{Err: fmt.Errorf("unknown help topic %q (topics: %s)",
					strings.Join(args, " "), strings.Join(topics, ", "))}
			}
			return target.Help()
		},
	}
}
