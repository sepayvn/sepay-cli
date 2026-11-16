package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type revokeOptions struct {
	all   bool
	token string // session id (from "auth sessions"), not a token value
}

// NewCmdRevoke builds "sepay auth revoke [profile]".
func NewCmdRevoke(f *cmdutil.Factory) *cobra.Command {
	opts := revokeOptions{}
	cmd := &cobra.Command{
		Use:   "revoke [profile]",
		Short: "Revoke a token server-side",
		Long: `Revoke an OAuth token server-side (effective within ~30s): a kill switch for
a leaked or CI token, even from another machine.

With no flag it revokes the active (or named) profile's own session. Use
--token <id> to revoke one session by id (list ids with "sepay auth sessions"),
or --all to revoke every session of the account. Unlike "logout", the profile is
kept: a revoked session shows as "revoked" in "sepay auth status" until you log in
again.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cmdutil.CompleteFirstArg(cmdutil.CompleteProfiles(f)),
		RunE: func(cmd *cobra.Command, args []string) error {
			// An unset shell var (--token "$ID") must not revoke the current session.
			if cmd.Flags().Changed("token") {
				opts.token = strings.TrimSpace(opts.token)
				if opts.token == "" {
					return &cmdutil.FlagError{Err: errors.New("--token needs a session id (see: sepay auth sessions)")}
				}
			}
			var profile string
			if len(args) == 1 {
				profile = args[0]
			}
			return runRevoke(cmd.Context(), f, &opts, profile)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&opts.all, "all", false, "Revoke every session of the account")
	fl.StringVar(&opts.token, "token", "", "Revoke one session by id (see: sepay auth sessions)")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnGuidance:    "--all prompts first: it ends every session of the account on every machine, including CI, in sandbox and production alike. A revoked token cannot be un-revoked; the holder must log in again.",
		cmdutil.AnnConfirms:    "conditional",
	})
}

func runRevoke(ctx context.Context, f *cmdutil.Factory, opts *revokeOptions, profile string) error {
	if opts.all && opts.token != "" {
		return &cmdutil.FlagError{Err: errors.New("--all and --token are mutually exclusive")}
	}

	switch {
	case opts.token != "":
		host, token, err := f.TokenForProfile(profile)
		if err != nil {
			return err
		}
		client := f.NewOAuthClient(host)
		// Asked first: once revoked, this token can no longer list sessions.
		current := false
		if sessions, lerr := client.ListTokens(ctx, token); lerr == nil {
			for _, sess := range sessions {
				current = current || (sess.ID == opts.token && sess.Current)
			}
		}
		if _, err := client.RevokeTokenID(ctx, token, opts.token); err != nil {
			return authAPIError("revoke session", err)
		}
		if !current {
			f.Printf("%s Revoked session %s\n", f.SuccessIcon(), opts.token)
			return nil
		}
		forgetRevoked(f, revokeTarget(f, profile))
		f.Printf("%s Revoked session %s, the one this profile used; run: sepay login\n", f.SuccessIcon(), opts.token)
		return nil

	case opts.all:
		if ok, err := f.Confirm("revoke EVERY session of the account (all devices/CI)?"); err != nil {
			return err
		} else if !ok {
			return &cmdutil.FlagError{Err: errors.New("revoke --all cancelled")}
		}
		host, token, err := f.TokenForProfile(profile)
		if err != nil {
			return err
		}
		n, err := f.NewOAuthClient(host).RevokeAllTokens(ctx, token)
		if err != nil {
			return authAPIError("revoke sessions", err)
		}
		forgetRevoked(f, sameAccount(f, revokeTarget(f, profile))...)
		f.Printf("%s Revoked %d session(s); this token is now invalid, run: sepay login\n", f.SuccessIcon(), n)
		return nil

	default:
		mgr, err := f.Credentials()
		if err != nil {
			return err
		}
		hosts := mgr.Hosts()
		targets, err := targetProfiles(f, hosts, false, profile)
		if err != nil {
			return err
		}
		for _, name := range targets {
			p := hosts.Profiles[name]
			if p == nil {
				return cmdutil.UnknownProfileError(name)
			}
			cred, serr := mgr.Get(name)
			if serr != nil && !errors.Is(serr, auth.ErrNotFound) {
				return fmt.Errorf("could not read stored credential for %s: %w", name, serr)
			}
			if cred == nil {
				f.ErrPrintf("! no stored credential for profile %s; nothing to revoke\n", name)
				continue
			}
			if rerr := revokeCredential(ctx, f, p.Host, cred); rerr != nil {
				return authAPIError("revoke token", rerr)
			}
			forgetRevoked(f, name)
			f.Printf("%s Revoked token for profile %s; run: sepay login to sign in again\n", f.SuccessIcon(), name)
		}
		return nil
	}
}

func revokeTarget(f *cmdutil.Factory, explicit string) string {
	if explicit != "" {
		return explicit
	}
	mgr, err := f.Credentials()
	if err != nil {
		return ""
	}
	return f.ProfileName(mgr.Hosts())
}

// sameAccount returns name plus every profile with the same email on the same host.
func sameAccount(f *cmdutil.Factory, name string) []string {
	mgr, err := f.Credentials()
	if err != nil {
		return []string{name}
	}
	hosts := mgr.Hosts()
	target := hosts.Profiles[name]
	if target == nil || target.Email == "" {
		return []string{name}
	}
	names := []string{name}
	for _, other := range sortedProfileNames(hosts) {
		p := hosts.Profiles[other]
		if other != name && p.Email == target.Email && auth.SameHost(p.Host, target.Host) {
			names = append(names, other)
		}
	}
	return names
}

func forgetRevoked(f *cmdutil.Factory, names ...string) {
	mgr, err := f.Credentials()
	if err != nil {
		return
	}
	for _, name := range names {
		cred, err := mgr.Get(name)
		if err != nil || cred.Revoked() {
			continue
		}
		if err := mgr.MarkRevoked(name, cred); err != nil {
			f.ErrPrintf("! could not mark profile %s as revoked locally: %s\n", name, err.Error())
		}
	}
}
