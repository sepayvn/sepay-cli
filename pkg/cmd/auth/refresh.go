package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type refreshOptions struct {
	scopes       string
	removeScopes string
	resetScopes  bool
}

// NewCmdRefresh builds "sepay auth refresh".
func NewCmdRefresh(f *cmdutil.Factory) *cobra.Command {
	opts := refreshOptions{}
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Renew the token and adjust its scopes",
		Long: `Renew the access token for a profile, optionally narrowing its scopes.

Plain "auth refresh" just renews. --scopes adds to the current set, --remove-scopes
drops from it, and --reset-scopes keeps only the given --scopes. Narrowing is
one-way: a refresh can never widen past what was granted at login, so to regain a
dropped scope, log in again with sepay login --scopes read,webhook:write,...`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRefresh(cmd.Context(), f, &opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&opts.scopes, "scopes", "", "Scopes to request; added to the current set unless --reset-scopes (comma-separated)")
	fl.StringVar(&opts.removeScopes, "remove-scopes", "", "Scopes to drop (comma-separated)")
	fl.BoolVar(&opts.resetScopes, "reset-scopes", false, "Ignore current scopes; use only --scopes")
	return cmd
}

const refreshLockWait = 30 * time.Second

func runRefresh(ctx context.Context, f *cmdutil.Factory, opts *refreshOptions) error {
	// No API client is built here, so nothing else tells a typo from a missing login.
	if err := f.CheckExplicitProfile(); err != nil {
		return err
	}
	mgr, err := f.Credentials()
	if err != nil {
		return err
	}
	hosts := mgr.Hosts()
	name := f.ProfileName(hosts)
	p := hosts.Profiles[name]
	if name == "" || p == nil {
		return cmdutil.AuthError{Err: errors.New("not logged in, run: sepay login")}
	}
	cred, err := mgr.Get(name)
	if err != nil || cred.RefreshToken == "" {
		return cmdutil.AuthError{Err: fmt.Errorf("no refresh token for profile %s, run: sepay login", name)}
	}

	// mergeScopes drops unknown names, so check the flags. A scope already on the profile stays removable:
	// the server may grant one this build predates.
	added := parseScopes(opts.scopes)
	removed := parseScopes(opts.removeScopes)
	unknown := unknownScopes(added)
	unknown = append(unknown, slices.DeleteFunc(unknownScopes(removed), func(s string) bool {
		return slices.Contains(p.Scopes, s)
	})...)
	if len(unknown) > 0 {
		return &cmdutil.FlagError{Err: fmt.Errorf("unknown scope %s (valid: %s)",
			strings.Join(unknown, ", "), strings.Join(knownScopes, ", "))}
	}

	base := p.Scopes
	if opts.resetScopes {
		base = nil
	}
	target := mergeScopes(base, added, removed)
	// An empty set omits scope, which the server reads as "keep the original grant".
	if len(target) == 0 && len(p.Scopes) > 0 {
		return &cmdutil.FlagError{Err: errors.New("that would clear every scope, which a refresh cannot do; run: sepay logout, then sepay login --scopes <scopes>")}
	}

	// Another process may have rotated the token while this one waited for the lock.
	lockCtx, cancel := context.WithTimeout(ctx, refreshLockWait)
	release, _ := mgr.LockRefresh(lockCtx)
	cancel()
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if current, err := mgr.Current(name); err == nil {
		if current.RefreshToken == "" {
			return cmdutil.AuthError{Err: fmt.Errorf("no refresh token for profile %s, run: sepay login", name)}
		}
		cred = current
	}

	tok, err := f.NewOAuthClient(p.Host).Refresh(ctx, cred.RefreshToken, target)
	if err != nil {
		var ae *auth.Error
		if errors.As(err, &ae) && ae.Code == "invalid_scope" {
			msg := fmt.Sprintf("the server rejected the scope set; a refresh cannot widen beyond what was granted at login; run: sepay login --scopes %s", strings.Join(target, ","))
			if ae.Description != "" {
				msg = fmt.Sprintf("%s (%s)", msg, ae.Description)
			}
			return cmdutil.ScopeError{Err: errors.New(msg)}
		}
		return err
	}

	// A non-rotating endpoint may omit refresh_token; keep the stored one.
	if tok.RefreshToken == "" {
		tok.RefreshToken = cred.RefreshToken
	}

	granted := strings.Fields(tok.Scope)
	usedFile, err := mgr.SetIfCurrent(name, p.Host, cred, auth.CredentialFromToken(tok), granted, p.Insecure)
	if err != nil {
		var changed *auth.ProfileChangedError
		if errors.As(err, &changed) {
			// Signed in again elsewhere mid-exchange: nothing was stored.
			return fmt.Errorf("profile %s was signed in again while its token was being renewed; nothing was changed, re-run if you still want to adjust its scopes", name)
		}
		return fmt.Errorf("could not store credentials: %w", err)
	}
	if usedFile {
		f.WarnPlaintextStorage("your login", filepath.Join(mgr.Hosts().Dir(), config.HostsFile), false)
	}

	f.Printf("%s Refreshed token for %s (%s)\n",
		f.SuccessIcon(), cmdutil.DisplayHost(p.Host), strings.Join(granted, ", "))
	return nil
}
