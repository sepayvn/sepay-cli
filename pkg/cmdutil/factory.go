// Package cmdutil provides the shared Factory and error types used by every command.
package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
)

// ClientID identifies the CLI as the registered public OAuth client.
const ClientID = "sepay-cli"

const defaultOAuthHost = "my.sepay.vn"

// GlobalOptions holds persistent-flag values, read lazily by the Factory closures.
type GlobalOptions struct {
	Profile        string // --profile/-p
	Env            string // --env: "" (use config), "sandbox" or "production"
	Output         string // --output/-o: table|json|yaml
	Yes            bool   // --yes/-y
	Quiet          bool   // --quiet/-q
	Verbose        bool   // --verbose/-v
	NoColor        bool   // --no-color
	NoInput        bool   // --no-input
	ConfigPath     string // --config
	APIBase        string // --api-base
	IdempotencyKey string // --idempotency-key: reuse one key across attempts of the same mutation
}

// Factory supplies each command's IO, config and API clients.
type Factory struct {
	IOStreams *iostreams.IOStreams
	Global    *GlobalOptions

	// RawArgs is os.Args[1:]; HandleError reads the output format from it when flag parsing stopped early.
	RawArgs []string

	envBannerDone bool

	// stdinSecretFlag names the flag that already drained stdin.
	stdinSecretFlag string

	// staleTokenRefresh records a refresh that never reached the OAuth host.
	staleTokenRefresh error

	// keychainWarned keeps the "renewed token not saved" notice to one line per run.
	keychainWarned bool

	// Config, Credentials and NewAPIClient are lazy.
	Config       func() (*config.Config, error)
	Credentials  func() (*auth.Manager, error)
	NewAPIClient func() (*api.Client, error)

	// ProductManager brokers product-native credentials (pg/einvoice/bankhub).
	ProductManager func() (*product.Manager, error)

	// NewPGClient, NewBankHubClient and NewEInvoiceClient build product v1 clients on broker-minted credentials.
	NewPGClient func() (*pg.Client, error)

	NewBankHubClient func() (*bankhub.Client, error)

	NewEInvoiceClient func() (*einvoice.Client, error)

	// NewOAuthClient builds a device-flow client for host; SEPAY_OAUTH_URL overrides it.
	NewOAuthClient func(host string) *auth.Client
}

// New builds the real Factory wired to the OS environment.
func New() *Factory {
	io := iostreams.System()
	f := &Factory{IOStreams: io, Global: &GlobalOptions{}, RawArgs: os.Args[1:]}

	var cachedCfg *config.Config
	f.Config = func() (*config.Config, error) {
		if cachedCfg != nil {
			return cachedCfg, nil
		}
		var c *config.Config
		var err error
		if f.Global.ConfigPath != "" {
			c, err = config.LoadFrom(f.Global.ConfigPath)
		} else {
			c, err = config.Load()
		}
		if err != nil {
			return nil, err
		}
		cachedCfg = c
		return c, nil
	}

	var cachedMgr *auth.Manager
	f.Credentials = func() (*auth.Manager, error) {
		if cachedMgr != nil {
			return cachedMgr, nil
		}
		var hosts *config.Hosts
		var err error
		if f.Global.ConfigPath != "" {
			hosts, err = config.LoadHostsFrom(filepath.Dir(f.Global.ConfigPath))
		} else {
			hosts, err = config.LoadHosts()
		}
		if err != nil {
			return nil, err
		}
		f.warnLoosePerms(io, hosts.Dir())
		cachedMgr = auth.NewManager(hosts)
		return cachedMgr, nil
	}

	f.NewOAuthClient = func(host string) *auth.Client {
		base := os.Getenv("SEPAY_OAUTH_URL")
		if base != "" {
			// An insecure or non-SePay override would leak the refresh token: fall back to the default.
			if err := ValidateCredentialOrigin(base); err != nil {
				fmt.Fprintf(io.ErrOut, "! ignoring insecure SEPAY_OAUTH_URL: %v\n", err)
				base = ""
			} else if err := AssertOAuthHost(base, "SEPAY_OAUTH_URL"); err != nil {
				fmt.Fprintf(io.ErrOut, "! %v\n", err)
				base = ""
			}
		}
		if base == "" {
			// A poisoned hosts.yml must not redirect the refresh token.
			base = OAuthBaseURL(host)
			if err := ValidateCredentialOrigin(base); err != nil {
				fmt.Fprintf(io.ErrOut, "! ignoring insecure oauth host: %v\n", err)
				base = OAuthBaseURL("")
			} else if err := AssertOAuthHost(base, "oauth host"); err != nil {
				fmt.Fprintf(io.ErrOut, "! ignoring non-SePay oauth host %q; using %s\n", host, defaultOAuthHost)
				base = OAuthBaseURL("")
			}
		}
		return auth.NewClient(base, ClientID, nil)
	}

	f.NewAPIClient = func() (*api.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		if err := f.CheckExplicitProfile(); err != nil {
			return nil, err
		}

		env, baseURL, source := f.apiBaseURL(cfg)
		if err := ValidateCredentialOrigin(baseURL); err != nil {
			// Never echo baseURL: the error is already redacted.
			return nil, fmt.Errorf("invalid API base from %s: %w", source, err)
		}
		if err := AssertCredentialHost(env, baseURL, source, baseURLForEnv(env)); err != nil {
			return nil, err
		}
		f.printEnvBanner(env, baseURL, source)

		token := f.resolveToken()
		if token == "" {
			// Without a token the 401 path would wrongly say "session expired".
			return nil, f.NotLoggedInError()
		}
		client := api.NewClient(baseURL, token)
		client.Reauth = f.renewRejectedToken
		client.Logf = func(format string, a ...any) {
			fmt.Fprintf(io.ErrOut, format+"\n", a...)
		}
		return client, nil
	}

	f.ProductManager = func() (*product.Manager, error) {
		client, err := f.NewAPIClient()
		if err != nil {
			return nil, err
		}
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		mgr, err := f.Credentials()
		if err != nil {
			return nil, err
		}
		store := product.NewStore(cfg.Dir())
		profile := f.ProfileName(mgr.Hosts())
		pm := product.NewManager(product.NewBroker(client), store, profile, f.resolveEnv(cfg))
		// Product credentials follow the storage the profile chose at login, never a silent fallback.
		if p := mgr.Hosts().Profiles[profile]; p != nil && p.Insecure {
			pm.SetInsecure(true)
		}
		return pm, nil
	}

	f.NewPGClient = func() (*pg.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		env, baseURL, source := f.pgBaseURL(cfg)
		if err := ValidateCredentialOrigin(baseURL); err != nil {
			// Never echo baseURL: the error is already redacted.
			return nil, fmt.Errorf("invalid API base from %s: %w", source, err)
		}
		if err := AssertCredentialHost(env, baseURL, source, pgBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		// PG resolves its own base, so the noun-level sandbox guard does not cover it.
		if err := f.AssertSandboxBase(env, baseURL, source, pgBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		// checkout/init uses the hosted-checkout host, gated separately.
		_, checkoutURL, checkoutSource := f.pgCheckoutURL(cfg)
		if err := ValidateCredentialOrigin(checkoutURL); err != nil {
			return nil, fmt.Errorf("invalid checkout base from %s: %w", checkoutSource, err)
		}
		if err := AssertCredentialHost(env, checkoutURL, checkoutSource, payBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		if err := f.AssertSandboxBase(env, checkoutURL, checkoutSource, payBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		f.printEnvBanner(env, baseURL, source)
		mgr, err := f.ProductManager()
		if err != nil {
			return nil, err
		}
		client := pg.NewClient(baseURL, mgr)
		client.CheckoutURL = checkoutURL
		client.Logf = func(format string, a ...any) {
			fmt.Fprintf(io.ErrOut, format+"\n", a...)
		}
		return client, nil
	}

	f.NewBankHubClient = func() (*bankhub.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		env, baseURL, source := f.bankHubBaseURL(cfg)
		if err := ValidateCredentialOrigin(baseURL); err != nil {
			// Never echo baseURL: the error is already redacted.
			return nil, fmt.Errorf("invalid API base from %s: %w", source, err)
		}
		if err := AssertCredentialHost(env, baseURL, source, bankHubBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		// Bank Hub resolves its own base, so the noun-level sandbox guard does not cover it.
		if err := f.AssertSandboxBase(env, baseURL, source, bankHubBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		f.printEnvBanner(env, baseURL, source)
		mgr, err := f.ProductManager()
		if err != nil {
			return nil, err
		}
		client := bankhub.NewClient(baseURL, mgr)
		client.Logf = func(format string, a ...any) {
			fmt.Fprintf(io.ErrOut, format+"\n", a...)
		}
		return client, nil
	}

	f.NewEInvoiceClient = func() (*einvoice.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		env, baseURL, source := f.einvoiceBaseURL(cfg)
		if err := ValidateCredentialOrigin(baseURL); err != nil {
			// Never echo baseURL: the error is already redacted.
			return nil, fmt.Errorf("invalid API base from %s: %w", source, err)
		}
		if err := AssertCredentialHost(env, baseURL, source, einvoiceBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		// E-Invoice resolves its own base, so the noun-level sandbox guard does not cover it.
		if err := f.AssertSandboxBase(env, baseURL, source, einvoiceBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		f.printEnvBanner(env, baseURL, source)
		mgr, err := f.ProductManager()
		if err != nil {
			return nil, err
		}
		client := einvoice.NewClient(baseURL, mgr)
		client.Logf = func(format string, a ...any) {
			fmt.Fprintf(io.ErrOut, format+"\n", a...)
		}
		return client, nil
	}

	return f
}

// warnLoosePerms warns (never refuses) when a credential file is readable beyond its owner.
// Not silenced by --quiet or -o json: the CI runs that write hosts.yml from a secret set those.
func (f *Factory) warnLoosePerms(io *iostreams.IOStreams, dir string) {
	for _, name := range []string{config.HostsFile, product.ProductsFile} {
		if loose, err := config.LoosePerms(dir, name); err == nil && loose {
			fmt.Fprintf(io.ErrOut, "! %s is readable by other users; run: chmod 600 %s\n",
				name, filepath.Join(dir, name))
		}
	}
}

func (f *Factory) resolveEnv(cfg *config.Config) string {
	if f.Global.Env != "" {
		return f.Global.Env
	}
	return cfg.Get("default_env")
}

// ActiveEnv returns the environment the next API call targets.
func (f *Factory) ActiveEnv() string {
	if f.Global.Env != "" {
		return f.Global.Env
	}
	if f.Config == nil {
		return config.EnvSandbox
	}
	cfg, err := f.Config()
	if err != nil {
		return config.EnvSandbox
	}
	return f.resolveEnv(cfg)
}

// resolvedAPIHost returns the normalized API host; ok is false when it has no parseable host.
func (f *Factory) resolvedAPIHost() (host string, ok bool) {
	env := f.ActiveEnv()
	var baseURL string
	switch {
	case f.Global.APIBase != "":
		baseURL = f.Global.APIBase
	case envHostOverride(env, "API_URL") != "":
		baseURL = envHostOverride(env, "API_URL")
	default:
		if f.Config != nil {
			if cfg, err := f.Config(); err == nil {
				_, baseURL, _ = f.apiBaseURL(cfg)
			}
		}
		if baseURL == "" {
			baseURL = baseURLForEnv(env)
		}
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	return normalizeHost(u.Hostname()), true
}

// isProductionTarget is default-deny: anything but the sandbox host or loopback counts as production.
func (f *Factory) isProductionTarget() bool {
	if f.ActiveEnv() == config.EnvProduction {
		return true
	}
	host, ok := f.resolvedAPIHost()
	if !ok {
		return true
	}
	return !sandboxSafeHost(host)
}

// Target returns a "host (env)" summary of the next API call's destination.
func (f *Factory) Target() string {
	env := f.ActiveEnv()
	if host, ok := f.resolvedAPIHost(); ok {
		return fmt.Sprintf("%s (%s)", host, env)
	}
	return env
}

// ConfirmProdWrite requires confirmation for a write on production; --yes skips the prompt, never OTP.
func (f *Factory) ConfirmProdWrite(action string) error {
	if !f.isProductionTarget() {
		return nil
	}
	// action may carry server or user text; an escape could rewrite the prompt.
	action = jsonfmt.Clean(action)
	fmt.Fprintf(f.IOStreams.ErrOut, "! live: %s targets PRODUCTION\n", action)
	ok, err := f.Confirm(fmt.Sprintf("%s on production?", action))
	if err != nil {
		return err
	}
	if !ok {
		return &FlagError{Err: fmt.Errorf("%s cancelled", action)}
	}
	return nil
}

// IdempotencyKey returns the --idempotency-key value, validated against the server's charset, or a fresh key.
func (f *Factory) IdempotencyKey() (string, error) {
	key := strings.TrimSpace(f.Global.IdempotencyKey)
	if key == "" {
		return api.NewIdempotencyKey()
	}
	if !idempotencyKeyPattern.MatchString(key) {
		return "", &FlagError{Err: fmt.Errorf("--idempotency-key must be 8-255 characters of A-Z a-z 0-9 . _ -")}
	}
	return key, nil
}

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,255}$`)

// ConfirmMutation is the confirmation gate every mutating command calls.
func (f *Factory) ConfirmMutation(action string, destructive bool) error {
	if f.isProductionTarget() {
		return f.ConfirmProdWrite(action)
	}
	if !destructive {
		return nil
	}
	action = jsonfmt.Clean(action)
	ok, err := f.Confirm(action + "?")
	if err != nil {
		return err
	}
	if !ok {
		return &FlagError{Err: fmt.Errorf("%s cancelled", action)}
	}
	return nil
}

func pgBaseURLForEnv(env string) string {
	if env == config.EnvProduction {
		return "https://pgapi.sepay.vn"
	}
	return "https://pgapi-sandbox.sepay.vn"
}

// payBaseURLForEnv is the PG hosted-checkout host, distinct from the REST API host.
func payBaseURLForEnv(env string) string {
	if env == config.EnvProduction {
		return "https://pay.sepay.vn"
	}
	return "https://pay-sandbox.sepay.vn"
}

// ProfileName returns the profile to act on: --profile > SEPAY_PROFILE > active profile.
func (f *Factory) ProfileName(hosts *config.Hosts) string {
	name := f.Global.Profile
	if name == "" {
		name = os.Getenv("SEPAY_PROFILE")
	}
	if name == "" {
		name = hosts.Active
	}
	return name
}

// Account identifies the authenticated account as email@host, falling back to the profile name.
func (f *Factory) Account() string {
	if f.Credentials == nil {
		return ""
	}
	mgr, err := f.Credentials()
	if err != nil {
		return ""
	}
	hosts := mgr.Hosts()
	name := f.ProfileName(hosts)
	if p := hosts.Profiles[name]; p != nil && p.Email != "" {
		return p.Email + "\x1f" + p.Host
	}
	return name
}

// CheckExplicitProfile fails with "unknown profile" when a named profile has no entry.
func (f *Factory) CheckExplicitProfile() error {
	name := f.Global.Profile
	if name == "" {
		name = os.Getenv("SEPAY_PROFILE")
	}
	if name == "" {
		return nil
	}
	mgr, err := f.Credentials()
	if err != nil {
		return nil // credential store unavailable: resolveToken and the API handle it
	}
	if _, ok := mgr.Hosts().Profiles[name]; !ok {
		return UnknownProfileError(name)
	}
	return nil
}

// NotLoggedInError returns the error for a run with no usable credential.
func (f *Factory) NotLoggedInError() error {
	if mgr, err := f.Credentials(); err == nil {
		hosts := mgr.Hosts()
		name := f.ProfileName(hosts)
		if name == "" && len(hosts.Profiles) > 0 {
			return AuthError{Err: errors.New("no active profile, run: sepay auth switch <profile>")}
		}
		if cred, err := mgr.Get(name); err == nil && cred.Revoked() {
			return AuthError{Err: errors.New("session revoked, run: sepay login")}
		}
	}
	return AuthError{Err: errors.New("not logged in, run: sepay login")}
}

// ErrTokenRefreshUnreachable marks a refresh that never reached the OAuth host; the grant may still be valid.
var ErrTokenRefreshUnreachable = errors.New("could not reach the OAuth host to refresh the access token")

// errRefreshExchange marks a failed exchange, as opposed to a failure storing the new token.
var errRefreshExchange = errors.New("token exchange failed")

// StaleTokenRefresh reports whether the last client's token went out stale because refresh was unreachable.
func (f *Factory) StaleTokenRefresh() error { return f.staleTokenRefresh }

// refusedTheGrant: only a transport failure (*url.Error) means the host was never reached; an unreadable
// answer counts as refused so a revoked grant behind a proxy still ends the session. 408, 429 and 5xx settle nothing.
func refusedTheGrant(err error) bool {
	var transport *url.Error
	if errors.As(err, &transport) {
		return false
	}

	var e *auth.Error
	if !errors.As(err, &e) {
		return true
	}

	if e.StatusCode >= 500 || e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests {
		return false
	}

	return e.StatusCode >= 400
}

// grantIsGone is narrower than refusedTheGrant because it deletes the stored token.
func grantIsGone(err error) bool {
	var e *auth.Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == "invalid_grant" || (e.StatusCode == http.StatusUnauthorized && e.Code == "invalid_request")
}

// resolveToken returns the chosen profile's access token, refreshing an expired one first.
func (f *Factory) resolveToken() string {
	f.staleTokenRefresh = nil

	mgr, err := f.Credentials()
	if err != nil {
		return ""
	}
	name := f.ProfileName(mgr.Hosts())
	cred, err := mgr.Get(name)
	if err != nil {
		return ""
	}
	fresh, refreshErr := f.refreshIfExpired(mgr, name, cred)
	f.noteStaleRefresh(refreshErr)
	if fresh != "" {
		return fresh
	}
	if grantIsGone(refreshErr) {
		return ""
	}
	return cred.AccessToken
}

// renewRejectedToken is the API client's Reauth hook.
func (f *Factory) renewRejectedToken(ctx context.Context, rejected string) string {
	mgr, err := f.Credentials()
	if err != nil {
		return ""
	}
	name := f.ProfileName(mgr.Hosts())
	cred, err := mgr.Get(name)
	if err != nil || cred.RefreshToken == "" {
		return ""
	}
	if cred.AccessToken != rejected && !cred.Expired() {
		return cred.AccessToken
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	fresh, err := f.refresh(ctx, mgr, name, cred)
	f.noteStaleRefresh(err)
	return fresh
}

func (f *Factory) noteStaleRefresh(err error) {
	if errors.Is(err, errRefreshExchange) && !refusedTheGrant(err) {
		f.staleTokenRefresh = fmt.Errorf("%w: %v", ErrTokenRefreshUnreachable, err)
	}
}

// TokenForProfile returns the OAuth host and a valid access token for a profile, refreshing if lapsed.
func (f *Factory) TokenForProfile(explicit string) (host, accessToken string, err error) {
	mgr, err := f.Credentials()
	if err != nil {
		return "", "", err
	}
	hosts := mgr.Hosts()
	name := explicit
	if name == "" {
		name = f.ProfileName(hosts)
	}
	cred, err := mgr.Get(name)
	if err != nil || cred.AccessToken == "" {
		return "", "", AuthError{Err: errors.New("not logged in, run: sepay login")}
	}
	token := cred.AccessToken
	if fresh, _ := f.refreshIfExpired(mgr, name, cred); fresh != "" {
		token = fresh
	}
	return hosts.Profiles[name].Host, token, nil
}

const refreshTimeout = 30 * time.Second

// refreshIfExpired returns "" on any failure, so the caller sends the stored token and gets a 401.
func (f *Factory) refreshIfExpired(mgr *auth.Manager, profile string, cred *auth.Credential) (string, error) {
	if !cred.Expired() || cred.RefreshToken == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	return f.refresh(ctx, mgr, profile, cred)
}

func (f *Factory) refresh(ctx context.Context, mgr *auth.Manager, profile string, cred *auth.Credential) (string, error) {
	p := mgr.Hosts().Profiles[profile]

	// The exchange runs outside the config lock; SetIfCurrent settles a concurrent re-login or rotation at write time.
	tok, err := f.NewOAuthClient(p.Host).Refresh(ctx, cred.RefreshToken, p.Scopes)
	if err != nil {
		if grantIsGone(err) {
			_ = mgr.MarkRevoked(profile, cred)
		}
		return "", fmt.Errorf("%w: %w", errRefreshExchange, err)
	}
	// A non-rotating endpoint may omit refresh_token: keep the stored one.
	if tok.RefreshToken == "" {
		tok.RefreshToken = cred.RefreshToken
	}
	if _, err := mgr.SetIfCurrent(profile, p.Host, cred, auth.CredentialFromToken(tok), strings.Fields(tok.Scope), p.Insecure); err != nil {
		var changed *auth.ProfileChangedError
		if errors.As(err, &changed) && changed.Held != nil && !changed.Held.Expired() {
			return changed.Held.AccessToken, nil
		}
		if errors.Is(err, auth.ErrKeychainUnavailable) {
			// Nothing reached the file. Use the new token for this run only, and say why the next
			// run may have to sign in again (the server may have rotated the refresh token).
			if !f.keychainWarned {
				f.keychainWarned = true
				f.ErrPrintf("! the renewed token is used for this run only and was not saved: %s\n", err.Error())
			}
			return tok.AccessToken, nil
		}
		return "", err
	}
	return tok.AccessToken, nil
}

// ProductStore returns the product-credential cache, evicted on login/logout so a reused label can't leak a prior tenant's.
func (f *Factory) ProductStore() (*product.Store, error) {
	if f.Config == nil {
		return nil, errors.New("config unavailable")
	}
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	return product.NewStore(cfg.Dir()), nil
}

// apiBaseURL resolves the API v2 host: --api-base > SEPAY_<ENV>_API_URL > config "<env>.api_url" > default.
func (f *Factory) apiBaseURL(cfg *config.Config) (env, baseURL, source string) {
	env = f.resolveEnv(cfg)
	if f.Global.APIBase != "" {
		return env, f.Global.APIBase, "--api-base"
	}
	if v := envHostOverride(env, "API_URL"); v != "" {
		return env, v, "SEPAY_" + strings.ToUpper(env) + "_API_URL"
	}
	if v := cfg.Get(env + ".api_url"); v != "" {
		return env, v, "config"
	}
	return env, baseURLForEnv(env), "default"
}

func (f *Factory) pgBaseURL(cfg *config.Config) (env, baseURL, source string) {
	env = f.resolveEnv(cfg)
	if v := envHostOverride(env, "PG_API_URL"); v != "" {
		return env, v, "SEPAY_" + strings.ToUpper(env) + "_PG_API_URL"
	}
	if v := cfg.Get(env + ".pg_api_url"); v != "" {
		return env, v, "config"
	}
	return env, pgBaseURLForEnv(env), "default"
}

// pgCheckoutURL never falls back to the pg_api_url override, which may not serve checkout/init.
func (f *Factory) pgCheckoutURL(cfg *config.Config) (env, baseURL, source string) {
	env = f.resolveEnv(cfg)
	if v := envHostOverride(env, "PG_CHECKOUT_URL"); v != "" {
		return env, v, "SEPAY_" + strings.ToUpper(env) + "_PG_CHECKOUT_URL"
	}
	if v := cfg.Get(env + ".pg_checkout_url"); v != "" {
		return env, v, "config"
	}
	return env, payBaseURLForEnv(env), "default"
}

func bankHubBaseURLForEnv(env string) string {
	if env == config.EnvProduction {
		return "https://bankhub-api.sepay.vn"
	}
	return "https://bankhub-api-sandbox.sepay.vn"
}

func (f *Factory) bankHubBaseURL(cfg *config.Config) (env, baseURL, source string) {
	env = f.resolveEnv(cfg)
	if v := envHostOverride(env, "BANKHUB_API_URL"); v != "" {
		return env, v, "SEPAY_" + strings.ToUpper(env) + "_BANKHUB_API_URL"
	}
	if v := cfg.Get(env + ".bankhub_api_url"); v != "" {
		return env, v, "config"
	}
	return env, bankHubBaseURLForEnv(env), "default"
}

func einvoiceBaseURLForEnv(env string) string {
	if env == config.EnvProduction {
		return "https://einvoice-api.sepay.vn"
	}
	return "https://einvoice-api-sandbox.sepay.vn"
}

func (f *Factory) einvoiceBaseURL(cfg *config.Config) (env, baseURL, source string) {
	env = f.resolveEnv(cfg)
	if v := envHostOverride(env, "EINVOICE_API_URL"); v != "" {
		return env, v, "SEPAY_" + strings.ToUpper(env) + "_EINVOICE_API_URL"
	}
	if v := cfg.Get(env + ".einvoice_api_url"); v != "" {
		return env, v, "config"
	}
	return env, einvoiceBaseURLForEnv(env), "default"
}

func envHostOverride(env, suffix string) string {
	return os.Getenv("SEPAY_" + strings.ToUpper(env) + "_" + suffix)
}

// printEnvBanner prints env and host once per run. Product builders must call it before minting,
// or the broker's v2 host wins the banner.
func (f *Factory) printEnvBanner(env, host, source string) {
	if f.envBannerDone || f.Global.Quiet || f.Global.Output != "table" {
		return
	}
	f.envBannerDone = true
	line := fmt.Sprintf("env: %s → %s", env, host)
	if source != "default" {
		line += " (" + source + ")"
	}
	fmt.Fprintln(f.IOStreams.ErrOut, f.IOStreams.Dim(line))
}

func baseURLForEnv(env string) string {
	if env == config.EnvProduction {
		return "https://cli-api.sepay.vn/v2"
	}
	return "https://cli-api-sandbox.sepay.vn/v2"
}
