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

type logoutOptions struct {
	all   bool
	local bool
}

// logoutResult is one profile's -o json|yaml outcome; Revoked only when the server confirmed.
type logoutResult struct {
	Profile string `json:"profile"`
	Host    string `json:"host"`
	Revoked bool   `json:"revoked"`
	Removed bool   `json:"removed"`
}

// NewCmdLogout builds "sepay auth logout" (also the root "sepay logout").
func NewCmdLogout(f *cmdutil.Factory) *cobra.Command {
	opts := logoutOptions{}
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out and forget stored credentials",
		Long: `Revoke the token server-side, then remove the stored credential locally.

When the server cannot confirm the revoke, the credential is kept so the logout
can be retried, and the command exits non-zero. --local removes it from this
machine without contacting the server; the session then stays live until it
expires or is revoked from another logged-in session ("sepay auth revoke --all").

Use --profile to target one profile, or --all for every stored profile (which
also revokes every session of each account server-side). To revoke a token
without a local copy (a leaked or CI token), use "sepay auth revoke".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogout(cmd.Context(), f, &opts)
		},
	}
	cmd.Flags().BoolVar(&opts.all, "all", false, "Log out of every stored profile")
	cmd.Flags().BoolVar(&opts.local, "local", false, "Remove the stored credential without revoking it server-side")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms: "conditional",
		cmdutil.AnnGuidance: "--all logs out of every stored profile and revokes each account's sessions; that one prompts. --local skips the server revoke and leaves the session live.",
	})
}

func runLogout(ctx context.Context, f *cmdutil.Factory, opts *logoutOptions) error {
	mgr, err := f.Credentials()
	if err != nil {
		return err
	}
	hosts := mgr.Hosts()

	targets, err := targetProfiles(f, hosts, opts.all, "")
	if err != nil {
		return err
	}
	if opts.all {
		if ok, cerr := f.Confirm("log out of EVERY stored profile (and revoke each account's sessions)?"); cerr != nil {
			return cerr
		} else if !ok {
			return &cmdutil.FlagError{Err: errors.New("logout --all cancelled")}
		}
	}

	covered := map[string]bool{} // profiles whose account a revoke-all already ended
	var kept, partial []string
	results := make([]logoutResult, 0, len(targets))
	for _, name := range targets {
		p := hosts.Profiles[name]
		if p == nil {
			return cmdutil.UnknownProfileError(name)
		}
		res := logoutResult{Profile: name, Host: cmdutil.DisplayHost(p.Host)}

		cred, serr := mgr.Get(name)
		if serr != nil && !errors.Is(serr, auth.ErrNotFound) && !opts.local {
			// Unreadable is not absent: the secret may still be live.
			f.ErrPrintf("! could not read stored credential for %s: %s\n", name, serr.Error())
			kept = append(kept, name)
			results = append(results, res)
			continue
		}

		// --all revokes every session of the account; ownGone: no stored token can still use this session.
		var rerr error
		ownGone := true
		switch {
		case opts.local:
			ownGone = false
		case opts.all && covered[name]:
			res.Revoked = true
		case opts.all && cred != nil && cred.AccessToken != "":
			ownGone, rerr = revokeAccount(ctx, f, name, p.Host)
			if rerr == nil {
				res.Revoked = true
				// Before mgr.Delete: sameAccount reads the stored profiles.
				for _, n := range sameAccount(f, name) {
					covered[n] = true
				}
			}
		case cred != nil:
			if rerr = revokeCredential(ctx, f, p.Host, cred); rerr == nil {
				res.Revoked = true
			} else {
				ownGone = false
			}
		}
		if rerr != nil {
			f.ErrPrintf("! could not revoke token server-side for %s: %s\n", name, rerr.Error())
		}
		if rerr != nil && !ownGone {
			kept = append(kept, name)
			results = append(results, res)
			continue
		}

		if derr := mgr.Delete(name); derr != nil {
			return fmt.Errorf("could not remove stored credential for %s: %w", name, derr)
		}
		res.Removed = true
		// Cached product credentials never expire; logout must not leave them behind.
		if ps, perr := f.ProductStore(); perr == nil {
			// Warn, never block. On a file-fallback profile a keychain error means "no secret service".
			if eerr := ps.EvictProfile(name); eerr != nil && !p.Insecure {
				f.ErrPrintf("! could not clear the cached product credentials of profile %q; do not reuse this label until it is resolved: %s\n",
					name, eerr.Error())
			}
		}
		switch {
		case opts.local:
			f.ErrPrintf("! profile %s removed from this machine only; its session stays live on %s until it expires or is revoked from a logged-in session: sepay auth revoke --all\n",
				name, res.Host)
		case rerr != nil:
			f.ErrPrintf("! removed profile %s locally, but its account's other sessions may still be live\n", name)
			partial = append(partial, name)
		}
		results = append(results, res)
	}

	if err := f.RenderOutput(results, func() error {
		for _, r := range results {
			if r.Revoked && r.Removed {
				f.Printf("%s Logged out of %s (profile %s)\n", f.SuccessIcon(), r.Host, r.Profile)
			} else if r.Removed && opts.local {
				f.Printf("%s Removed profile %s from this machine\n", f.SuccessIcon(), r.Profile)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if hosts.Active == "" && len(hosts.Profiles) > 0 {
		fmt.Fprintln(f.IOStreams.ErrOut, "no active profile; run: sepay auth switch <profile>")
	}
	retry := "sepay logout"
	if opts.all {
		retry = "sepay logout --all"
	}
	return logoutFailure(retry, kept, partial)
}

// logoutFailure names kept profiles (credential still live here) and partial ones (other sessions may be live).
func logoutFailure(retry string, kept, partial []string) error {
	var msgs []string
	if len(kept) > 0 {
		msgs = append(msgs, fmt.Sprintf("could not revoke the session of %s, so it is still stored here; retry when the server is reachable: %s, or remove it from this machine only: %s --local",
			strings.Join(kept, ", "), retry, retry))
	}
	if len(partial) > 0 {
		msgs = append(msgs, fmt.Sprintf("could not revoke every session of %s; if you already logged out everywhere from another device, nothing more is needed, otherwise log in again and run: sepay logout --all",
			strings.Join(partial, ", ")))
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New(strings.Join(msgs, "; "))
}

// revokeAccount ends every session of a profile's account, falling back to this machine's own grant.
// ownGone reports whether this machine's grant is dead.
func revokeAccount(ctx context.Context, f *cmdutil.Factory, name, host string) (ownGone bool, err error) {
	_, token, err := f.TokenForProfile(name)
	if err == nil {
		if _, err = f.NewOAuthClient(host).RevokeAllTokens(ctx, token); err == nil {
			return true, nil
		}
	}
	// The refresh in TokenForProfile may have rotated the refresh token.
	mgr, merr := f.Credentials()
	if merr != nil {
		return false, err
	}
	cred, gerr := mgr.Get(name)
	if gerr != nil {
		return errors.Is(gerr, auth.ErrNotFound), err
	}
	if rerr := revokeCredential(ctx, f, host, cred); rerr != nil {
		return false, fmt.Errorf("%w (this machine's token was not revoked either: %v)", err, rerr)
	}
	return true, err
}
