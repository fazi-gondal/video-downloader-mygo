// Package netguard restricts outbound HTTP requests to public destinations.
// It validates the URL, rejects local/private/link-local hosts, pins DNS
// results into the dialer to prevent rebinding, and rechecks every redirect.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maxRedirects = 10

// ErrNonPublicDestination marks a URL or redirect that resolves to a blocked
// address. Callers can map it to a user-facing privacy message.
var ErrNonPublicDestination = errors.New("destination is not public")

// ValidURL reports whether u is an http(s) URL with a public-looking hostname
// and no embedded credentials. IP literals in blocked ranges are rejected here;
// hostnames are resolved and checked again by the transport.
func ValidURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := normalizedHostname(u.Hostname())
	if isBlockedHostname(host) {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
		return false
	}
	return true
}

// WrapClient returns a copy of client whose transport pins DNS results and
// whose redirect policy revalidates every target and caps the chain. The
// caller's timeout and other settings are preserved. A nil client uses the
// default transport.
func WrapClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	wrapped := *client
	wrapped.Transport = NewTransport(client.Transport)
	previous := client.CheckRedirect
	wrapped.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !ValidURL(req.URL) {
			return ErrNonPublicDestination
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &wrapped
}

type resolvedIPs struct {
	host string
	ips  []net.IP
}

type resolvedIPsKey struct{}

type guardedTransport struct {
	base      http.RoundTripper
	transport *http.Transport
	closeIdle bool
}

// NewTransport wraps base so every request first resolves the hostname and
// rejects any non-public address. When base is a standard transport, the
// dialer is replaced with one that connects only to the resolved addresses.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return &guardedTransport{base: base}
	}

	clone := transport.Clone()
	originalDial := clone.DialContext
	if originalDial == nil {
		dialer := &net.Dialer{}
		originalDial = dialer.DialContext
	}
	secure := &guardedTransport{base: clone, transport: clone, closeIdle: true}
	clone.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		resolved, _ := ctx.Value(resolvedIPsKey{}).(*resolvedIPs)
		if resolved == nil {
			return originalDial(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(normalizedHostname(host), resolved.host) {
			// A proxy transport may dial the proxy rather than the target host.
			return originalDial(ctx, network, address)
		}

		var lastErr error
		for _, ip := range resolved.ips {
			dialNetwork := network
			if network == "tcp" {
				if ip.To4() != nil {
					dialNetwork = "tcp4"
				} else {
					dialNetwork = "tcp6"
				}
			}
			conn, err := originalDial(ctx, dialNetwork, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	}
	return secure
}

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resolved, err := resolveIPs(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	if t.transport != nil {
		ctx := context.WithValue(req.Context(), resolvedIPsKey{}, resolved)
		req = req.WithContext(ctx)
	}
	return t.base.RoundTrip(req)
}

func (t *guardedTransport) CloseIdleConnections() {
	if t.closeIdle && t.transport != nil {
		t.transport.CloseIdleConnections()
	}
}

func resolveIPs(ctx context.Context, u *url.URL) (*resolvedIPs, error) {
	if !ValidURL(u) {
		return nil, errors.New("invalid network destination")
	}
	host := normalizedHostname(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		return &resolvedIPs{host: host, ips: []net.IP{ip}}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, errors.New("host resolution failed")
	}
	if len(ips) == 0 {
		return nil, errors.New("host has no addresses")
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return nil, ErrNonPublicDestination
		}
	}
	return &resolvedIPs{host: host, ips: ips}, nil
}

func normalizedHostname(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return host
}

func isBlockedHostname(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "local" || strings.HasSuffix(host, ".local") ||
		host == "home.arpa" || strings.HasSuffix(host, ".home.arpa")
}

var blockedNetworks = func() []*net.IPNet {
	var networks []*net.IPNet
	for _, cidr := range []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.88.99.0/24",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"224.0.0.0/4",
		"240.0.0.0/4",
		"::/128",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
		"ff00::/8",
		"2001:db8::/32",
	} {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			networks = append(networks, network)
		}
	}
	return networks
}()

func isBlockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return true
	}
	for _, network := range blockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
