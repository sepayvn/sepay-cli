// Package version implements "sepay version".
package version

import (
	"fmt"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// NewCmdVersion builds the "sepay version" command.
func NewCmdVersion(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build info",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return Print(f)
		},
	}
}

// Print writes the version banner, or version/commit/date under -o json|yaml.
func Print(f *cmdutil.Factory) error {
	ios := f.IOStreams
	switch f.Global.Output {
	case "json", "yaml":
		info := map[string]string{
			"version": build.Version,
			"commit":  build.Commit,
			"date":    build.Date,
		}
		if f.Global.Output == "yaml" {
			return jsonfmt.RenderYAML(ios.Out, info)
		}
		return jsonfmt.RenderJSON(ios.Out, info)
	}
	fmt.Fprintln(ios.Out, build.String())
	return nil
}
