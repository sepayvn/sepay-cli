// Package docs implements "sepay docs", the embedded reference for agents.
package docs

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// content holds the embedded topic files.
//
//go:embed content/*.md
var content embed.FS

// NewCmdDocs creates the `sepay docs` command.
func NewCmdDocs(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs [topic]",
		Short: "Print SePay reference topics",
		Long: `Print a reference topic as Markdown on stdout.

Run without a topic to list what is available. The topics ship inside the
binary, so they always match the version you are running.

They cover this CLI. Every platform contract — payload fields, response rules,
retry curves, signatures — lives on developer.sepay.vn and is linked rather than
copied, so it cannot go stale here. Append ` + "`.md`" + ` to any page URL for raw Markdown;
` + "`https://developer.sepay.vn/llms.txt`" + ` indexes every page.`,
		Args: cobra.MaximumNArgs(1),
		Example: `  sepay docs
  sepay docs overview
  sepay docs flows`,
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return topics(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listTopics(f)
			}
			return printTopic(f, args[0])
		},
	}
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnGuidance: "Read `sepay docs glossary` before writing a script against an unfamiliar noun, " +
			"and `sepay docs webhook-payload` / `sepay docs ipn-payload` before writing a handler. " +
			"Platform contracts are linked to developer.sepay.vn, not restated — fetch the `.md` form of a page.",
	})
}

func topics() []string {
	entries, err := fs.ReadDir(content, "content")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}

func listTopics(f *cmdutil.Factory) error {
	names := topics()
	return f.RenderOutput(names, func() error {
		out := f.IOStreams.Out
		fmt.Fprintln(out, "Topics:")
		for _, t := range names {
			fmt.Fprintf(out, "  %s\n", t)
		}
		fmt.Fprintln(out, "\nRun `sepay docs <topic>` to print one.")
		return nil
	})
}

// Topic returns a topic's raw Markdown. The name is matched against the embedded list, never joined into a path.
func Topic(name string) ([]byte, error) {
	available := topics()
	for _, t := range available {
		if t == name {
			return content.ReadFile("content/" + name + ".md")
		}
	}
	return nil, &cmdutil.FlagError{Err: fmt.Errorf(
		"unknown topic %q (available: %s)", name, strings.Join(available, ", "))}
}

func printTopic(f *cmdutil.Factory, name string) error {
	body, err := Topic(name)
	if err != nil {
		return err
	}
	_, err = f.IOStreams.Out.Write(body)
	return err
}
