package auth

import (
	"fmt"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdToken builds "sepay auth token".
func NewCmdToken(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the stored OAuth2 refresh token",
		Long: `Print the stored refresh token — the only command that prints a token. It
rotates on use: the printed value stops working as soon as sepay renews the
session, so it is not a reusable secret. Use it to move this session to another
machine with "sepay login --with-token".`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runToken(f)
		},
	}
}

func runToken(f *cmdutil.Factory) error {
	// No API client here, so a misspelled --profile would otherwise read as "not logged in".
	if err := f.CheckExplicitProfile(); err != nil {
		return err
	}
	mgr, err := f.Credentials()
	if err != nil {
		return err
	}
	hosts := mgr.Hosts()
	name := f.ProfileName(hosts)
	if name == "" || hosts.Profiles[name] == nil {
		return f.NotLoggedInError()
	}
	cred, err := mgr.Get(name)
	if err != nil || cred.RefreshToken == "" {
		return cmdutil.AuthError{Err: fmt.Errorf("no refresh token for profile %s, run: sepay login", name)}
	}
	fmt.Fprintln(f.IOStreams.Out, cred.RefreshToken)
	return nil
}
