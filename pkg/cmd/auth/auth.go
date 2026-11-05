// Package auth implements "sepay auth" and the root "login"/"logout" aliases.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdAuth builds the "sepay auth" parent command.
func NewCmdAuth(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth <command>",
		Short: "Login, profiles and OAuth tokens",
		Long: `Log in to SePay, inspect stored profiles, and manage OAuth tokens.

Login uses the OAuth 2.1 device flow: sepay prints a code, you approve it in the
browser, and the token is stored in your OS keychain. Without a usable keychain
(a server, a container), login keeps it in a 0600 file instead and says so;
--insecure-storage chooses the file up front.
Tokens are never printed except by "sepay auth token".`,
	}
	cmd.AddCommand(NewCmdLogin(f))
	cmd.AddCommand(NewCmdStatus(f))
	cmd.AddCommand(NewCmdSessions(f))
	cmd.AddCommand(NewCmdSwitch(f))
	cmd.AddCommand(NewCmdRefresh(f))
	cmd.AddCommand(NewCmdToken(f))
	cmd.AddCommand(NewCmdRevoke(f))
	cmd.AddCommand(NewCmdLogout(f))
	return cmd
}

func parseScopes(csv string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func sortedProfileNames(hosts *config.Hosts) []string {
	names := make([]string, 0, len(hosts.Profiles))
	for name := range hosts.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// targetProfiles resolves the profiles to act on: all, explicit, or the active one.
func targetProfiles(f *cmdutil.Factory, hosts *config.Hosts, all bool, explicit string) ([]string, error) {
	if all {
		names := sortedProfileNames(hosts)
		if len(names) == 0 {
			return nil, cmdutil.AuthError{Err: errors.New("no stored profiles, run: sepay login")}
		}
		return names, nil
	}
	name := explicit
	if name == "" {
		name = f.ProfileName(hosts)
	}
	if name == "" {
		return nil, cmdutil.AuthError{Err: errors.New("not logged in, run: sepay login")}
	}
	if hosts.Profiles[name] == nil {
		return nil, cmdutil.UnknownProfileError(name)
	}
	return []string{name}, nil
}

// revokeCredential best-effort revokes cred on host, preferring the refresh token: revoke cascades from it,
// while an expired access token would leave the refresh token alive behind a silent 200.
func revokeCredential(ctx context.Context, f *cmdutil.Factory, host string, cred *auth.Credential) error {
	if cred == nil {
		return nil
	}
	token, hint := cred.RefreshToken, auth.TokenTypeRefresh
	if token == "" {
		token, hint = cred.AccessToken, auth.TokenTypeAccess
	}
	if token == "" {
		return nil
	}
	return f.NewOAuthClient(host).Revoke(ctx, token, hint)
}

// authAPIError maps a 401 to "session expired" (exit 4) and wraps anything else under action.
func authAPIError(action string, err error) error {
	var ae *auth.Error
	if errors.As(err, &ae) && ae.StatusCode == http.StatusUnauthorized {
		return cmdutil.AuthError{Err: errors.New("session expired, run: sepay login")}
	}
	return fmt.Errorf("could not %s: %w", action, err)
}

// unknownScopes catches scope typos as usage errors instead of a server invalid_scope.
func unknownScopes(scopes []string) []string {
	var unknown []string
	for _, s := range scopes {
		if !slices.Contains(knownScopes, s) {
			unknown = append(unknown, s)
		}
	}
	return unknown
}

func mergeScopes(base, add, remove []string) []string {
	set := map[string]bool{}
	for _, s := range base {
		set[s] = true
	}
	for _, s := range add {
		set[s] = true
	}
	for _, s := range remove {
		delete(set, s)
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
