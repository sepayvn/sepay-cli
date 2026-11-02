package cmdutil

import (
	"fmt"
	"net"
	"net/url"

	"github.com/sepayvn/sepay-cli/internal/config"
)

func sandboxSafeHost(host string) bool {
	return sandboxSafeHostFor(host, sandboxAPIHost())
}

// sandboxSafeHostFor allows only canonicalHost and loopback. host must be pre-normalized.
func sandboxSafeHostFor(host, canonicalHost string) bool {
	return host == canonicalHost || isLoopbackHost(host)
}

// AssertSandboxTarget refuses a sandbox command whose resolved API host is not a sandbox host,
// since --api-base and SEPAY_SANDBOX_API_URL can point it at production.
func (f *Factory) AssertSandboxTarget() error {
	baseURL, source := f.resolveSandboxBase()
	return assertSandboxHost(baseURL, source, sandboxAPIHost())
}

// AssertSandboxBase is AssertSandboxTarget for a product client's own base URL.
func (f *Factory) AssertSandboxBase(env, baseURL, source, canonicalBase string) error {
	if env != config.EnvSandbox {
		return nil
	}
	return assertSandboxHost(baseURL, source, hostOf(canonicalBase))
}

func assertSandboxHost(baseURL, source, canonicalHost string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return &FlagError{Err: fmt.Errorf("sandbox: cannot parse API host from %q (%s)", baseURL, source)}
	}
	host := normalizeHost(u.Hostname())
	if sandboxSafeHostFor(host, canonicalHost) {
		return nil
	}
	return &FlagError{Err: fmt.Errorf("sandbox commands refuse the non-sandbox host %q (from %s); expected %q or a loopback dev backend", host, source, canonicalHost)}
}

// UseSandbox pins a sandbox-only product to sandbox, rejecting --env production and production hosts.
func (f *Factory) UseSandbox(product string) error {
	if f.Global.Env == config.EnvProduction {
		return &FlagError{Err: fmt.Errorf("%s commands are sandbox-only; drop --env production", product)}
	}
	f.Global.Env = config.EnvSandbox
	return f.AssertSandboxTarget()
}

// AssertSandboxOnly refuses a mutation with no production endpoint yet; it never rewrites the env.
func (f *Factory) AssertSandboxOnly(command string) error {
	if f.ActiveEnv() == config.EnvProduction {
		return &FlagError{Err: fmt.Errorf("%s is sandbox-only; drop --env production", command)}
	}
	return f.AssertSandboxTarget()
}

// resolveSandboxBase mirrors apiBaseURL, honouring the env overrides when Config is absent (tests).
func (f *Factory) resolveSandboxBase() (baseURL, source string) {
	if f.Config != nil {
		if cfg, err := f.Config(); err == nil {
			_, baseURL, source = f.apiBaseURL(cfg)
			return baseURL, source
		}
	}
	switch {
	case f.Global.APIBase != "":
		return f.Global.APIBase, "--api-base"
	case envHostOverride(config.EnvSandbox, "API_URL") != "":
		return envHostOverride(config.EnvSandbox, "API_URL"), "SEPAY_SANDBOX_API_URL"
	default:
		return baseURLForEnv(config.EnvSandbox), "default"
	}
}

func sandboxAPIHost() string {
	return hostOf(baseURLForEnv(config.EnvSandbox))
}

func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return normalizeHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
