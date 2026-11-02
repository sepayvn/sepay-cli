package cmdutil

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NotImplementedError reports a command whose backend does not exist yet.
type NotImplementedError struct{ Path string }

func (e NotImplementedError) Error() string { return fmt.Sprintf("%q is not implemented yet", e.Path) }

// ExitCode returns ExitNotImplemented.
func (e NotImplementedError) ExitCode() ExitCode { return ExitNotImplemented }

// StubRunE is the RunE for a not-yet-built command.
func StubRunE(cmd *cobra.Command, _ []string) error {
	return NotImplementedError{Path: cmd.CommandPath()}
}

// Stub hides cmd from help but keeps it invocable (exit 3) and visible in `--map`.
func Stub(cmd *cobra.Command) *cobra.Command {
	cmd.Hidden = true
	cmd.RunE = StubRunE
	return Annotate(cmd, map[string]string{AnnImplemented: "no"})
}
