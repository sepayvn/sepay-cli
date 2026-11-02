package cmdutil

import (
	"errors"
	"fmt"
	"net/url"
)

// ValidateCredentialOrigin rejects an origin unsafe for a credential: no host, userinfo, a non-http(s)
// scheme, or http off loopback. Errors never echo rawURL, which may carry a secret; callers must not either.
func ValidateCredentialOrigin(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		// url.Parse's error repeats the URL.
		return &FlagError{Err: errors.New("invalid URL: could not be parsed")}
	}
	if u.Hostname() == "" {
		return &FlagError{Err: errors.New("URL has no host; include the scheme and host, e.g. https://my.sepay.vn")}
	}
	if u.User != nil {
		// Not u.Redacted(): it keeps the username, which is the secret in the <key>@host form.
		bare := *u
		bare.User = nil
		return &FlagError{Err: fmt.Errorf("URL %q must not contain userinfo (user:pass@); credentials come from sepay login, not the URL", bare.String())}
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return &FlagError{Err: fmt.Errorf("refusing to send credentials over plaintext http to non-loopback host %q; use https", u.Hostname())}
	default:
		// Scheme only: path and query can carry a token.
		return &FlagError{Err: fmt.Errorf("URL scheme %q is not supported; use http or https", u.Scheme)}
	}
}

// ValidateDestinationURL rejects a delivery URL the API is certain to refuse: unparsable, hostless,
// or not http(s). It does no DNS checks.
func ValidateDestinationURL(rawURL, name string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &FlagError{Err: fmt.Errorf("%s is not a valid URL: %w", name, err)}
	}
	if u.Hostname() == "" {
		return &FlagError{Err: fmt.Errorf("%s must include the scheme and host, e.g. https://example.com/hook", name)}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &FlagError{Err: fmt.Errorf("%s must use http or https", name)}
	}
	return nil
}
