package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// knownScopes is what login requests by default and refresh validates against. It is a subset of the server's
// DeviceVerification::PRODUCTION_ACTIONS (see TestKnownScopes_AreServerProductionActions).
var knownScopes = []string{"read", "webhook:write", "bank-account:write", "paycode:write"}

var defaultLoginScopes = strings.Join(knownScopes, ",")

type loginOptions struct {
	hostname  string
	scopes    string
	scopesSet bool
	withToken bool
	insecure  bool
}

// NewCmdLogin builds "sepay auth login" (also the root "sepay login").
func NewCmdLogin(f *cmdutil.Factory) *cobra.Command {
	opts := loginOptions{}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to SePay",
		Long: `Authenticate with SePay via the OAuth 2.1 device flow.

sepay prints a one-time code and opens your browser to approve it, then stores
the resulting token in your OS keychain. On a machine with no browser, pass
--with-token to read an OAuth2 refresh token from stdin instead.

The profile is named after your account email, so logging in as another account
creates its own profile; keep several side by side and switch with
"sepay auth switch <name>" or target one per command with --profile. Pass
--profile <name> to store this login under a custom alias instead. Where the
server returns no identity — which a --with-token exchange often does not — the
profile is named "default" and the EMAIL column stays empty, so pass --profile
there if you keep more than one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.scopesSet = cmd.Flags().Changed("scopes")
			return runLogin(cmd.Context(), f, &opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&opts.hostname, "hostname", "my.sepay.vn", "OAuth host to authenticate against: a host (https assumed) or a full URL")
	fl.StringVar(&opts.scopes, "scopes", defaultLoginScopes, "Capabilities to request (comma-separated)")
	fl.BoolVar(&opts.withToken, "with-token", false, "Read an OAuth2 refresh token from stdin (no browser)")
	fl.BoolVar(&opts.insecure, "insecure-storage", false, "Store the token in a 0600 file instead of the OS keychain")
	return cmd
}

func runLogin(ctx context.Context, f *cmdutil.Factory, opts *loginOptions) error {
	// Validate --hostname before storing it: a phished host must never receive the refresh token.
	// SEPAY_OAUTH_URL is validated in NewOAuthClient.
	if os.Getenv("SEPAY_OAUTH_URL") == "" {
		base := cmdutil.OAuthBaseURL(opts.hostname)
		if err := cmdutil.ValidateCredentialOrigin(base); err != nil {
			return err
		}
		if err := cmdutil.AssertOAuthHost(base, "--hostname"); err != nil {
			return err
		}
	}

	client := f.NewOAuthClient(opts.hostname)
	scopes := parseScopes(opts.scopes)

	var tok *auth.Token
	var err error
	if opts.withToken {
		// A refresh grant can only narrow; omitting scope keeps the original grant.
		if !opts.scopesSet {
			scopes = nil
		}
		tok, err = loginWithToken(ctx, f, client, scopes)
	} else {
		tok, err = loginDeviceFlow(ctx, f, client, scopes)
	}
	if err != nil {
		return err
	}

	mgr, err := f.Credentials()
	if err != nil {
		return err
	}
	profile := f.Global.Profile
	if profile == "" {
		profile = os.Getenv("SEPAY_PROFILE")
	}
	if profile == "" {
		profile = tok.Email
	}
	if profile == "" {
		profile = "default"
	}

	granted := strings.Fields(tok.Scope)
	// The issuing host, not --hostname: SEPAY_OAUTH_URL may override it.
	host := client.BaseURL()
	usedFile, err := mgr.Set(profile, host, auth.CredentialFromToken(tok), granted, opts.insecure, tok.Email)
	if err != nil {
		return fmt.Errorf("could not store credentials: %w", err)
	}
	warnInsecureStorage(f, usedFile)
	// The profile label may now belong to another tenant: evict its cached product credentials.
	if ps, perr := f.ProductStore(); perr == nil {
		// Warn, never block. On a file-storage login a keychain error usually means "no secret service".
		if eerr := ps.EvictProfile(profile); eerr != nil && !usedFile {
			f.ErrPrintf("! could not clear the cached product credentials of profile %q; a credential from its previous account may still be used: %s\n",
				profile, eerr.Error())
		}
	}
	if mgr.Hosts().Active != profile {
		if err := mgr.SetActive(profile); err != nil {
			return fmt.Errorf("could not activate profile: %w", err)
		}
	}

	return reportLogin(f, host, profile, tok.Email, granted)
}

func loginDeviceFlow(ctx context.Context, f *cmdutil.Factory, client *auth.Client, scopes []string) (*auth.Token, error) {
	ios := f.IOStreams
	da, pkce, err := client.RequestDeviceCode(ctx, scopes)
	if err != nil {
		return nil, err
	}

	// Never verification_uri_complete: the user must type the code, so a crafted link cannot auto-approve.
	uri := da.VerificationURI
	fmt.Fprintf(ios.ErrOut, "Open your browser to authenticate. Your code: %s\n", jsonfmt.Clean(da.UserCode))

	// verification_uri is server-supplied: open a browser only for a trusted origin, else print it.
	originErr := cmdutil.ValidateCredentialOrigin(uri)
	if originErr == nil {
		originErr = cmdutil.AssertOAuthHost(uri, "verification_uri")
	}
	switch {
	case originErr != nil:
		fmt.Fprintf(ios.ErrOut, "! not opening a browser: %v\n", originErr)
		fmt.Fprintf(ios.ErrOut, "Visit: %s\n", jsonfmt.Clean(uri))
	case ios.IsStdoutTTY() && cmdutil.OpenBrowser(uri):
		fmt.Fprintf(ios.ErrOut, "Opening %s\n", jsonfmt.Clean(uri))
	default:
		fmt.Fprintf(ios.ErrOut, "Visit: %s\n", jsonfmt.Clean(uri))
	}

	tok, err := client.PollToken(ctx, da.DeviceCode, pkce.Verifier,
		time.Duration(da.Interval)*time.Second, time.Duration(da.ExpiresIn)*time.Second)
	if err != nil {
		return nil, loginPollError(err)
	}
	return tok, nil
}

// loginWithToken exchanges a refresh token from stdin, for CI.
func loginWithToken(ctx context.Context, f *cmdutil.Factory, client *auth.Client, scopes []string) (*auth.Token, error) {
	// Reading a terminal would block silently; at a TTY the device flow is the path.
	if f.IOStreams.IsStdinTTY() {
		return nil, &cmdutil.FlagError{Err: errors.New("--with-token reads the token from stdin, but stdin is a terminal; pipe it in, or drop --with-token to use the browser flow")}
	}
	data, err := io.ReadAll(f.IOStreams.In)
	if err != nil {
		return nil, err
	}
	refresh := strings.TrimSpace(string(data))
	if refresh == "" {
		return nil, &cmdutil.FlagError{Err: errors.New("no token on stdin; pipe an OAuth2 refresh token")}
	}
	tok, err := client.Refresh(ctx, refresh, scopes)
	if err != nil {
		var ae *auth.Error
		// Same rejection runRefresh maps; the remedy here is the browser flow.
		if errors.As(err, &ae) && ae.Code == "invalid_scope" {
			msg := "the server rejected the scope set; --with-token exchanges a refresh token, which cannot widen beyond what was granted at login; drop --scopes to keep the token's own scopes, or re-consent with sepay login"
			if ae.Description != "" {
				msg = fmt.Sprintf("%s (%s)", msg, ae.Description)
			}
			return nil, cmdutil.ScopeError{Err: errors.New(msg)}
		}
		return nil, err
	}
	return tok, nil
}

func reportLogin(f *cmdutil.Factory, host, profile, email string, scopes []string) error {
	ios := f.IOStreams

	switch f.Global.Output {
	case "json":
		return jsonfmt.RenderJSON(ios.Out, loginResult(host, profile, email, scopes))
	case "yaml":
		return jsonfmt.RenderYAML(ios.Out, loginResult(host, profile, email, scopes))
	}
	who := email
	if who == "" {
		who = profile
	}
	fmt.Fprintf(ios.Out, "%s Logged in as %s\n", f.SuccessIcon(), jsonfmt.Clean(who))
	fmt.Fprintln(ios.ErrOut, ios.Dim("add another account with: sepay login --profile <name>"))
	return nil
}

func loginResult(host, profile, email string, scopes []string) map[string]any {
	return map[string]any{
		"logged_in": true,
		"host":      cmdutil.DisplayHost(host),
		"profile":   profile,
		"email":     email,
		"scopes":    scopes,
	}
}

func loginPollError(err error) error {
	var authErr *auth.Error
	if !errors.As(err, &authErr) {
		return err
	}
	switch authErr.Code {
	case auth.CodeAccessDenied:
		return errors.New("authorization denied")
	case auth.CodeExpiredToken:
		return errors.New("the login code expired, run: sepay login")
	}
	return err
}
