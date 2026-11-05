package cmdutil

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/config"
)

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// canonicalHost returns "" for an unparsable URL, so the gate fails closed.
func canonicalHost(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return normalizeHost(u.Hostname())
	}
	return ""
}

func oauthHostAllowed(host string) bool {
	return host == normalizeHost(defaultOAuthHost) || isLoopbackHost(host)
}

// AssertCredentialHost refuses to send a credential anywhere but canonical's host or loopback.
// Run after ValidateCredentialOrigin.
func AssertCredentialHost(rawURL, source, canonical string) error {
	host, err := credentialHost(rawURL, source)
	if err != nil {
		return err
	}
	if host == canonicalHost(canonical) || isLoopbackHost(host) {
		return nil
	}
	return credentialHostError(host, source, canonicalHost(canonical))
}

// OAuthBaseURL turns a bare host into an https base URL, keeping an explicit scheme.
func OAuthBaseURL(host string) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	switch {
	case host == "":
		return "https://" + defaultOAuthHost
	case strings.Contains(host, "://"):
		return host
	default:
		return "https://" + host
	}
}

// DisplayHost is the inverse of OAuthBaseURL: it drops an implicit https://.
func DisplayHost(host string) string {
	return strings.TrimPrefix(strings.TrimRight(strings.TrimSpace(host), "/"), "https://")
}

// hubHostForEnv must equal my.sepay.vn Config\SseHub::$publicUrl's host for env.
func hubHostForEnv(env string) string {
	if env == config.EnvProduction {
		return "ssehub.sepay.vn"
	}
	return "ssehub-sandbox.sepay.vn"
}

// AssertHubHost refuses to send a listen_key anywhere but env's SSE hub or loopback.
func AssertHubHost(env, rawURL, source string) error {
	host, err := credentialHost(rawURL, source)
	if err != nil {
		return err
	}
	expected := hubHostForEnv(env)
	if host == expected || isLoopbackHost(host) {
		return nil
	}
	return credentialHostError(host, source, expected+" ("+env+")")
}

// AssertOAuthHost is AssertCredentialHost for the env-independent OAuth host.
func AssertOAuthHost(rawURL, source string) error {
	host, err := credentialHost(rawURL, source)
	if err != nil {
		return err
	}
	if oauthHostAllowed(host) {
		return nil
	}
	return credentialHostError(host, source, "")
}

func credentialHost(rawURL, source string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "", &FlagError{Err: fmt.Errorf("cannot parse host from %q (%s)", rawURL, source)}
	}
	return normalizeHost(u.Hostname()), nil
}

// credentialHostError names the expected host, since the refused one may be another SePay host.
func credentialHostError(host, source, expected string) error {
	where := "a SePay host"
	if expected != "" {
		where = expected
	}
	return &FlagError{Err: fmt.Errorf("refusing to send credentials to host %q (from %s); expected %s or loopback", host, source, where)}
}

// checkoutHosts lists both envs: the page gets no credential, so the gate only stops a poisoned 302.
var checkoutHosts = map[string]bool{
	"pay.sepay.vn":         true,
	"pay-sandbox.sepay.vn": true,
}

// AssertCheckoutHost refuses to open a browser at a non-SePay checkout URL.
func AssertCheckoutHost(rawURL, source string) error {
	host, err := credentialHost(rawURL, source)
	if err != nil {
		return err
	}
	if checkoutHosts[host] || isLoopbackHost(host) {
		return nil
	}
	return &FlagError{Err: fmt.Errorf("not a SePay checkout host %q (from %s); expected pay.sepay.vn, pay-sandbox.sepay.vn or loopback", host, source)}
}
