package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// TransportError is a request that got no usable answer: the host never replied, or the reply broke off.
// Unwrap keeps the Go chain (*url.Error, net and TLS errors) reachable for callers that branch on it.
type TransportError struct {
	Host    string
	Timeout time.Duration
	Err     error
	// reading marks a failure after the headers arrived, while the body was being read.
	reading bool
}

// NewTransportError wraps err from sending a request to host with a client timeout of timeout.
func NewTransportError(host string, timeout time.Duration, err error) *TransportError {
	return &TransportError{Host: host, Timeout: timeout, Err: err}
}

// NewReadError wraps err from reading the body of an answer host had started to send.
func NewReadError(host string, err error) *TransportError {
	return &TransportError{Host: host, Err: fmt.Errorf("could not read server response: %w", err), reading: true}
}

func (e *TransportError) Unwrap() error { return e.Err }

func (e *TransportError) Error() string {
	if e.Cancelled() {
		return e.Reason()
	}
	return e.Reason() + "; check your internet connection and retry"
}

// Cancelled reports a request stopped by its caller (Ctrl-C), which says nothing about the network.
func (e *TransportError) Cancelled() bool { return errors.Is(e.Err, context.Canceled) }

// Reason names the cause in plain words, without the Go error chain.
func (e *TransportError) Reason() string {
	host := e.Host
	if host == "" {
		host = "SePay"
	}
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var netErr net.Error
	switch {
	case e.Cancelled():
		return fmt.Sprintf("the request to %s was cancelled", host)
	case errors.As(e.Err, &dnsErr):
		return fmt.Sprintf("cannot find %s (DNS lookup failed)", host)
	case errors.As(e.Err, &certErr), errors.As(e.Err, &unknownCA), errors.As(e.Err, &hostErr):
		return fmt.Sprintf("the secure connection to %s failed: its certificate is not trusted", host)
	case isConnRefused(e.Err):
		return fmt.Sprintf("%s refused the connection", host)
	case isNoRoute(e.Err):
		return fmt.Sprintf("no network route to %s", host)
	case errors.Is(e.Err, context.DeadlineExceeded), errors.As(e.Err, &netErr) && netErr.Timeout():
		if e.Timeout > 0 {
			return fmt.Sprintf("%s did not answer within %s", host, e.Timeout)
		}
		return fmt.Sprintf("%s did not answer in time", host)
	case e.reading, isConnReset(e.Err), errors.Is(e.Err, io.ErrUnexpectedEOF), errors.Is(e.Err, io.EOF):
		return fmt.Sprintf("the connection to %s was cut off", host)
	default:
		return fmt.Sprintf("cannot reach %s", host)
	}
}
