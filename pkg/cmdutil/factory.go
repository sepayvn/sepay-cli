package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	"github.com/spf13/cobra"
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

	// Prompter answers interactive prompts; nil draws them on the terminal.
	Prompter Prompter

	// UsageCommand is the path of the command that failed (e.g. "sepay transaction get"), named in
	// the usage hint; empty means the root.
	UsageCommand string

	envBannerDone bool

	// commandHost resolves the host the running command reaches; nil for a command that reaches none.
	// It lets the env banner come before the command's first prompt.
	commandHost func(*config.Config) (env, baseURL, source string)
	// commandHostKey is a product command's host override key (BANKHUB_API_URL…), empty for the
	// rest. Its credential is minted at the API host with the CLI token, so the banner also names
	// an overridden API host.
	commandHostKey string
	// commandCallsCheckout is set for a command that also posts to the PG hosted-checkout host.
	commandCallsCheckout bool

	// stdinSecretFlag names the flag that already drained stdin.
	stdinSecretFlag string

	// staleTokenRefresh records a refresh that never reached the OAuth host.
	staleTokenRefresh error

	// keychainWarned keeps the "renewed token not saved" notice to one line per run.
	keychainWarned bool

	// runCtx is the running command's context. NewAPIClient takes none, yet a renewal waiting on the
	// refresh lock must still stop on Ctrl-C.
	runCtx context.Context

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
	// Globals: flag parsing fills f.Global after New returns, so every closure reads it at call time.
	ios := iostreams.System()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}, RawArgs: os.Args[1:]}

	// Stores: config.yml and hosts.yml load once per run, beside --config when it is set.
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
		f.warnLoosePerms(ios, hosts.Dir())
		cachedMgr = auth.NewManager(hosts)
		return cachedMgr, nil
	}

	// Token resolution: refresh and renewal go through this client, so its host is checked first.
	f.NewOAuthClient = func(host string) *auth.Client {
		base := os.Getenv("SEPAY_OAUTH_URL")
		if base != "" {
			// An insecure or non-SePay override would leak the refresh token: fall back to the default.
			if err := ValidateCredentialOrigin(base); err != nil {
				fmt.Fprintf(ios.ErrOut, "! ignoring insecure SEPAY_OAUTH_URL: %v\n", err)
				base = ""
			} else if err := AssertOAuthHost(base, "SEPAY_OAUTH_URL"); err != nil {
				fmt.Fprintf(ios.ErrOut, "! %v\n", err)
				base = ""
			}
		}
		if base == "" {
			// A poisoned hosts.yml must not redirect the refresh token.
			base = OAuthBaseURL(host)
			if err := ValidateCredentialOrigin(base); err != nil {
				fmt.Fprintf(ios.ErrOut, "! ignoring insecure oauth host: %v\n", err)
				base = OAuthBaseURL("")
			} else if err := AssertOAuthHost(base, "oauth host"); err != nil {
				fmt.Fprintf(ios.ErrOut, "! ignoring non-SePay oauth host %q; using %s\n", host, defaultOAuthHost)
				base = OAuthBaseURL("")
			}
		}
		return auth.NewClient(base, ClientID, nil)
	}

	// Client factories: each checks its base against the credential host before a token rides on it.
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
		if err := AssertCredentialHost(baseURL, source, baseURLForEnv(env)); err != nil {
			return nil, err
		}
		f.printEnvBanner(f.IOStreams.ErrOut, env, source, "")

		token := f.resolveToken()
		if token == "" {
			// Without a token the 401 path would wrongly say "session expired".
			return nil, f.NotLoggedInError()
		}
		client := api.NewClient(baseURL, token)
		client.Reauth = f.renewRejectedToken
		client.Logf = f.progressf
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
		if err := AssertCredentialHost(baseURL, source, pgBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		// PG resolves its own base, so the noun-level sandbox guard does not cover it.
		if err := f.AssertSandboxBase(env, baseURL, source, pgBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		// checkout/init uses the hosted-checkout host, gated separately.
		checkoutURL, checkoutSource := f.pgCheckoutURL(cfg)
		if err := ValidateCredentialOrigin(checkoutURL); err != nil {
			return nil, fmt.Errorf("invalid checkout base from %s: %w", checkoutSource, err)
		}
		if err := AssertCredentialHost(checkoutURL, checkoutSource, payBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		if err := f.AssertSandboxBase(env, checkoutURL, checkoutSource, payBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		f.printEnvBanner(f.IOStreams.ErrOut, env, "", f.apiSource(cfg), f.ownHosts(cfg, source, "PG_API_URL")...)
		mgr, err := f.ProductManager()
		if err != nil {
			return nil, err
		}
		client := pg.NewClient(baseURL, mgr)
		client.CheckoutURL = checkoutURL
		client.Logf = f.progressf
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
		if err := AssertCredentialHost(baseURL, source, bankHubBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		// Bank Hub resolves its own base, so the noun-level sandbox guard does not cover it.
		if err := f.AssertSandboxBase(env, baseURL, source, bankHubBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		f.printEnvBanner(f.IOStreams.ErrOut, env, "", f.apiSource(cfg), f.ownHosts(cfg, source, "BANKHUB_API_URL")...)
		mgr, err := f.ProductManager()
		if err != nil {
			return nil, err
		}
		client := bankhub.NewClient(baseURL, mgr)
		client.Logf = f.progressf
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
		if err := AssertCredentialHost(baseURL, source, einvoiceBaseURLForEnv(env)); err != nil {
			return nil, err
		}
		// E-Invoice resolves its own base, so the noun-level sandbox guard does not cover it.
		if err := f.AssertSandboxBase(env, baseURL, source, einvoiceBaseURLForEnv(config.EnvSandbox)); err != nil {
			return nil, err
		}
		f.printEnvBanner(f.IOStreams.ErrOut, env, "", f.apiSource(cfg), f.ownHosts(cfg, source, "EINVOICE_API_URL")...)
		mgr, err := f.ProductManager()
		if err != nil {
			return nil, err
		}
		client := einvoice.NewClient(baseURL, mgr)
		client.Logf = f.progressf
		return client, nil
	}

	return f
}

// warnLoosePerms warns (never refuses) when a credential file is readable beyond its owner.
// Not silenced by --quiet or -o json: the CI runs that write hosts.yml from a secret set those.
func (f *Factory) warnLoosePerms(ios *iostreams.IOStreams, dir string) {
	for _, name := range []string{config.HostsFile, product.ProductsFile} {
		if loose, err := config.LoosePerms(dir, name); err == nil && loose {
			fmt.Fprintf(ios.ErrOut, "! %s is readable by other users; run: chmod 600 %s\n",
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

// IsProductionTarget reports whether the next API call may reach production; see isProductionTarget.
func (f *Factory) IsProductionTarget() bool { return f.isProductionTarget() }

// Target returns a "host (env)" summary of the next API call's destination.
func (f *Factory) Target() string {
	env := f.ActiveEnv()
	if host, ok := f.resolvedAPIHost(); ok {
		return fmt.Sprintf("%s (%s)", host, env)
	}
	return env
}

// ConfirmOut is where a confirm's warning, and what a command shows ahead of it, is written: the
// prompt's own stream when a question will be asked (after the env line, so the warning stays next
// to its question), stderr otherwise.
func (f *Factory) ConfirmOut() io.Writer {
	if f.Global.Yes || !f.canPrompt() {
		return f.IOStreams.ErrOut
	}
	f.bannerBeforePrompt()
	return f.promptOut()
}

// ConfirmProdWrite requires confirmation for a write on production; --yes skips the prompt, never OTP.
func (f *Factory) ConfirmProdWrite(action string) error {
	if !f.isProductionTarget() {
		return nil
	}
	// action may carry server or user text; an escape could rewrite the prompt.
	action = jsonfmt.Clean(action)
	// Under --json or -o yaml stderr carries only the error object, and --yes already confirmed.
	if f.Global.Output != "json" && f.Global.Output != "yaml" {
		fmt.Fprintf(f.ConfirmOut(), "! live: %s targets PRODUCTION\n", action)
	}
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

const refreshTimeout = 30 * time.Second

// SetContext gives the factory the running command's context, for work started outside a command's own calls.
func (f *Factory) SetContext(ctx context.Context) { f.runCtx = ctx }

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
func (f *Factory) pgCheckoutURL(cfg *config.Config) (baseURL, source string) {
	env := f.resolveEnv(cfg)
	if v := envHostOverride(env, "PG_CHECKOUT_URL"); v != "" {
		return v, "SEPAY_" + strings.ToUpper(env) + "_PG_CHECKOUT_URL"
	}
	if v := cfg.Get(env + ".pg_checkout_url"); v != "" {
		return v, "config"
	}
	return payBaseURLForEnv(env), "default"
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

// progressf reports a client's progress (a 429 wait, a network retry) on stderr. Under
// --quiet, --json or -o yaml it stays silent unless --verbose asks for it: machine output keeps
// stderr for the error object alone.
func (f *Factory) progressf(format string, a ...any) {
	if !f.Global.Verbose && (f.Global.Quiet || f.Global.Output != "table") {
		return
	}
	fmt.Fprintf(f.IOStreams.ErrOut, format+"\n", a...)
}

// ownHost is a host a product command calls besides the API host: its banner label, where it came
// from and its override key.
type ownHost struct{ label, source, key string }

// ownHosts lists the product hosts the running command calls: its own and, for checkout init, the
// hosted-checkout host.
func (f *Factory) ownHosts(cfg *config.Config, source, key string) []ownHost {
	hosts := []ownHost{{"host", source, key}}
	if f.commandCallsCheckout {
		_, checkoutSource := f.pgCheckoutURL(cfg)
		hosts = append(hosts, ownHost{"checkout host", checkoutSource, "PG_CHECKOUT_URL"})
	}
	return hosts
}

// printEnvBanner prints the env once per run, plus where each host came from when it is not the
// built-in default; the URL itself is left to --verbose. source is the API host's for an API
// command, empty for a product command, which passes its own hosts instead and apiSource, where the
// API host its credential is minted at came from, since the CLI token goes there too. Product
// builders must call it before minting, or the broker's banner, which names only the API host,
// comes first.
func (f *Factory) printEnvBanner(w io.Writer, env, source, apiSource string, own ...ownHost) {
	if f.envBannerDone || f.Global.Quiet || f.Global.Output != "table" {
		return
	}
	f.envBannerDone = true
	line := "env: " + env
	if source != "" && source != "default" {
		line += " · host from " + source
	}
	var unmovedLabels, unmovedKeys []string
	for _, h := range own {
		if h.source != "default" {
			line += " · " + h.label + " from " + h.source
			continue
		}
		unmovedLabels = append(unmovedLabels, h.label)
		unmovedKeys = append(unmovedKeys, "SEPAY_"+strings.ToUpper(env)+"_"+h.key)
	}
	if apiSource != "" && apiSource != "default" {
		line += " · API host from " + apiSource
	}
	fmt.Fprintln(w, f.IOStreams.Dim(line))
	// --api-base reads as "send this command there", but it leaves the product hosts where they are.
	if apiSource == "--api-base" && len(unmovedKeys) > 0 {
		pronoun := "it"
		if len(unmovedKeys) > 1 {
			pronoun = "them"
		}
		fmt.Fprintln(w, f.IOStreams.Dim(fmt.Sprintf(
			"note: --api-base moves only the API host; this command still calls its default %s (set %s to move %s)",
			strings.Join(unmovedLabels, " and "), strings.Join(unmovedKeys, " and "), pronoun)))
	}
}

// apiSource is where the API host came from.
func (f *Factory) apiSource(cfg *config.Config) string {
	_, _, source := f.apiBaseURL(cfg)
	return source
}

// BindCommand records which hosts cmd reaches, chosen by its top-level noun: Bank Hub, PG and
// E-Invoice resolve their own, local commands none. A command annotated AnnHost overrides its noun.
func (f *Factory) BindCommand(cmd *cobra.Command) {
	noun := cmd
	for noun.HasParent() && noun.Parent().HasParent() {
		noun = noun.Parent()
	}
	f.commandHostKey = ""
	f.commandCallsCheckout = false
	host := Annotation(cmd, AnnHost)
	if host == HostAPI {
		f.commandHost = f.apiBaseURL
		return
	}
	switch noun.Name() {
	case "bankhub":
		f.commandHost, f.commandHostKey = f.bankHubBaseURL, "BANKHUB_API_URL"
	case "pg":
		f.commandHost, f.commandHostKey = f.pgBaseURL, "PG_API_URL"
		f.commandCallsCheckout = host == HostPGCheckout
	case "einvoice":
		f.commandHost, f.commandHostKey = f.einvoiceBaseURL, "EINVOICE_API_URL"
	case "auth", "login", "logout", "config", "completion", "version", "sepay":
		f.commandHost = nil
	default:
		f.commandHost = f.apiBaseURL
	}
}

// bannerBeforePrompt prints the env banner ahead of the first question, so the answers are read
// against the environment they go to.
func (f *Factory) bannerBeforePrompt() {
	if f.envBannerDone || f.commandHost == nil || f.Config == nil {
		return
	}
	cfg, err := f.Config()
	if err != nil {
		return
	}
	env, _, source := f.commandHost(cfg)
	if f.commandHostKey == "" {
		f.printEnvBanner(f.promptOut(), env, source, "")
		return
	}
	f.printEnvBanner(f.promptOut(), env, "", f.apiSource(cfg), f.ownHosts(cfg, source, f.commandHostKey)...)
}

func baseURLForEnv(env string) string {
	if env == config.EnvProduction {
		return "https://cli-api.sepay.vn/v2"
	}
	return "https://cli-api-sandbox.sepay.vn/v2"
}
