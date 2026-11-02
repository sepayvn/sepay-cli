// Package completion implements "sepay completion".
package completion

import (
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdCompletion builds the "sepay completion" command.
func NewCmdCompletion(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "Generate a shell completion script",
		Long: `Generate a completion script for the given shell.

Bash:
  # requires the bash-completion package (apt install bash-completion / brew install bash-completion)
  echo 'source <(sepay completion bash)' >> ~/.bashrc

Zsh:
  # enable completion first if you haven't: echo 'autoload -U compinit; compinit' >> ~/.zshrc
  echo 'source <(sepay completion zsh)' >> ~/.zshrc

Fish:
  sepay completion fish > ~/.config/fish/completions/sepay.fish

PowerShell:
  sepay completion powershell | Out-String | Invoke-Expression

Open a new shell for the changes to take effect.`,
		// The error must list the shells: ExactArgs names none and usage is silenced.
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &cmdutil.FlagError{Err: fmt.Errorf("pick one shell: %s", strings.Join(cmd.ValidArgs, ", "))}
			}
			return cobra.OnlyValidArgs(cmd, args)
		},
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := f.IOStreams.Out
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			default:
				return root.GenPowerShellCompletionWithDesc(out)
			}
		},
	}
}
