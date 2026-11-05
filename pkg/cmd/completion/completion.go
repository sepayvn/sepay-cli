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
		Long: `Generate a completion script for the given shell and print it on stdout.

Write the script to a file once, as below; the shell loads it from there, so
sepay does not run at every shell start. Homebrew installs the bash, zsh and fish
scripts for you.

Bash (needs the bash-completion package):
  mkdir -p ~/.local/share/bash-completion/completions
  sepay completion bash > ~/.local/share/bash-completion/completions/sepay

Zsh (then add fpath+=~/.zfunc to ~/.zshrc, before compinit):
  mkdir -p ~/.zfunc
  sepay completion zsh > ~/.zfunc/_sepay

Fish:
  sepay completion fish > ~/.config/fish/completions/sepay.fish

PowerShell (add the line to $PROFILE):
  sepay completion powershell | Out-String | Invoke-Expression

Open a new shell for the changes to take effect.`,
		Example: `  # bash, for the current user
  sepay completion bash > ~/.local/share/bash-completion/completions/sepay

  # print the zsh script to inspect it
  sepay completion zsh | less`,
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
