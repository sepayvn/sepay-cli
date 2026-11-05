package auth

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdSwitch builds "sepay auth switch <profile>", prompting when no argument is given.
func NewCmdSwitch(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:               "switch <profile>",
		Aliases:           []string{"sw"},
		Short:             "Switch the active profile",
		Long:              "Change which stored profile later commands use by default.\n\n<profile> is a name \"sepay auth status\" lists. Prompted for on a terminal; required otherwise.",
		Example:           `  sepay auth switch staging`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cmdutil.CompleteFirstArg(cmdutil.CompleteProfiles(f)),
		RunE: func(_ *cobra.Command, args []string) error {
			profile := ""
			if len(args) > 0 {
				profile = args[0]
			}
			return runSwitch(f, profile)
		},
	}
}

func runSwitch(f *cmdutil.Factory, profile string) error {
	mgr, err := f.Credentials()
	if err != nil {
		return err
	}
	if profile == "" {
		if profile, err = pickProfile(f, mgr.Hosts()); err != nil {
			return err
		}
	}
	if err := mgr.SetActive(profile); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return cmdutil.UnknownProfileError(profile)
		}
		return err
	}
	f.Printf("%s Switched to profile %s\n", f.SuccessIcon(), profile)
	return nil
}

func pickProfile(f *cmdutil.Factory, hosts *config.Hosts) (string, error) {
	if len(hosts.Profiles) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no stored profiles, run: sepay login")}
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a profile name is required")}
	}
	names := make([]string, 0, len(hosts.Profiles))
	for name := range hosts.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	choices := make([]cmdutil.PromptChoice, 0, len(names))
	for _, name := range names {
		label := name
		if p := hosts.Profiles[name]; p != nil && p.Email != "" && p.Email != name {
			label += " · " + p.Email
		}
		if name == hosts.Active {
			label += " (active)"
		}
		choices = append(choices, cmdutil.PromptChoice{Label: label, Value: name})
	}
	return f.SelectChoiceOrFlag("", "profile", "Profile", choices)
}
