package accounts

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	oai "github.com/sashabaranov/go-openai"
)

// Response body caps per client.
const (
	ChatBodyCap  = 1 << 20
	EmbedBodyCap = 16 << 20
	CIMDBodyCap  = 64 << 10
)

// Fixed user-facing provider error texts. Upstream bodies and addresses are
// never included.
var (
	ErrProviderUnreachable = errors.New("provider unreachable")
	ErrProviderRejectedKey = errors.New("provider rejected the key")
	ErrProviderUnexpected  = errors.New("unexpected response")
	errBodyTooLarge        = errors.New("response too large")
	errBlockedAddress      = errors.New("address not allowed")
	errInsecureScheme      = errors.New("https required")
)

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
var zeroNet = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(8, 32)}

// blockedIP reports whether ip is loopback, private, link-local, CGNAT,
// ULA, unspecified, multicast or an IPv4-mapped form of those.
func blockedIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() ||
		cgnat.Contains(ip) || zeroNet.Contains(ip)
}

// SafeClient returns the hardened client for provider and CIMD traffic.
// Unless allowPrivate: addresses are checked at dial time (so DNS rebinding
// is covered) and only https is spoken. Redirects are off, environment
// proxies are ignored, and response bodies are capped at maxBody.
func SafeClient(allowPrivate bool, maxBody int64) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errBlockedAddress
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip) {
				return errBlockedAddress
			}
			return nil
		}
	}
	// ResponseHeaderTimeout stays long: a non-streaming chat completion on a
	// large prompt only sends headers once generation is finished.
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport:     &capTransport{base: tr, max: maxBody, requireHTTPS: !allowPrivate},
		Timeout:       180 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type capTransport struct {
	base         http.RoundTripper
	max          int64
	requireHTTPS bool
}

func (c *capTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if c.requireHTTPS && r.URL.Scheme != "https" {
		return nil, errInsecureScheme
	}
	resp, err := c.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > c.max {
		resp.Body.Close()
		return nil, errBodyTooLarge
	}
	resp.Body = &capBody{rc: resp.Body, left: c.max}
	return resp, nil
}

// capBody delivers at most max bytes, then fails.
type capBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *capBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, errBodyTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *capBody) Close() error { return b.rc.Close() }

// ProviderError maps any provider failure to one of the fixed texts.
func ProviderError(err error) error {
	if err == nil {
		return nil
	}
	var dm DimsMismatchError
	if errors.As(err, &dm) {
		return dm
	}
	if errors.Is(err, ErrProviderUnreachable) || errors.Is(err, ErrProviderRejectedKey) || errors.Is(err, ErrProviderUnexpected) {
		return err
	}
	status := 0
	var ae *oai.APIError
	var re *oai.RequestError
	var ane *anthropic.Error
	switch {
	case errors.As(err, &ae):
		status = ae.HTTPStatusCode
	case errors.As(err, &re):
		status = re.HTTPStatusCode
	case errors.As(err, &ane):
		status = ane.StatusCode
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrProviderRejectedKey
	}
	if status != 0 {
		return ErrProviderUnexpected
	}
	if errors.Is(err, errBodyTooLarge) {
		return ErrProviderUnexpected
	}
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, errBlockedAddress) ||
		errors.Is(err, errInsecureScheme) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrProviderUnreachable
	}
	return ErrProviderUnexpected
}

// DimsMismatchError reports an embedding model that returned vectors of a
// different size than the settings ask for (e.g. a gateway that ignores the
// dimensions parameter). Its text names only the two sizes, never upstream
// content, so it is safe to show the user.
type DimsMismatchError struct{ Got, Want int }

func (e DimsMismatchError) Error() string {
	return fmt.Sprintf("the embedding model returned %d dimensions, but Dimensions is set to %d. "+
		"Set Dimensions to %d, or use a model that supports %d", e.Got, e.Want, e.Got, e.Want)
}
