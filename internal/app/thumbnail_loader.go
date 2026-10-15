package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
)

const (
	maxThumbnailBytes  = 8 << 20
	maxThumbnailPixels = 16 << 20
	maxThumbnailWidth  = 640
)

// fetchThumbnail loads a bounded image without retaining request or URL state.
func fetchThumbnail(ctx context.Context, client *http.Client, rawURL string) (*ui.Bitmap, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !validThumbnailURL(parsed) {
		return nil, errors.New("invalid thumbnail URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("invalid thumbnail request")
	}
	if client == nil {
		client = http.DefaultClient
	}
	// Preserve the caller's transport, timeout and redirect policy without
	// mutating a client that may be shared by concurrent loaders.
	requestClient := *client
	requestClient.Transport = newThumbnailTransport(client.Transport)
	requestClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !validThumbnailURL(req.URL) {
			return errors.New("invalid thumbnail redirect")
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many thumbnail redirects")
		}
		return nil
	}
	resp, err := requestClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// net/http errors can embed signed URLs or credentials.
		return nil, errors.New("thumbnail request failed")
	}
	defer requestClient.CloseIdleConnections()
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("thumbnail HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxThumbnailBytes {
		return nil, errors.New("thumbnail exceeds byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("reading thumbnail failed")
	}
	if len(data) > maxThumbnailBytes {
		return nil, errors.New("thumbnail exceeds byte limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid thumbnail image")
	}
	// Division avoids overflowing on attacker-controlled dimensions.
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxThumbnailPixels/config.Height {
		return nil, errors.New("thumbnail exceeds pixel limit")
	}
	if config.Width > maxThumbnailWidth {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("decoding thumbnail failed")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		width := maxThumbnailWidth
		height := max(1, int(int64(config.Height)*maxThumbnailWidth/int64(config.Width)))
		resized := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.ApproxBiLinear.Scale(resized, resized.Bounds(), img, img.Bounds(), draw.Src, nil)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, resized); err != nil {
			return nil, errors.New("encoding thumbnail failed")
		}
		data = encoded.Bytes()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bitmap, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil, errors.New("decoding thumbnail failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bitmap, nil
}

func validThumbnailURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := normalizedThumbnailHostname(u.Hostname())
	if isBlockedThumbnailHostname(host) {
		return false
	}
	if ip := parseThumbnailIP(host); ip != nil && isBlockedThumbnailIP(ip) {
		return false
	}
	return true
}

type thumbnailResolvedIPs struct {
	host string
	ips  []net.IP
}

type thumbnailResolvedIPsKey struct{}

type thumbnailTransport struct {
	base      http.RoundTripper
	transport *http.Transport
	closeIdle bool
}

func newThumbnailTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return &thumbnailTransport{base: base}
	}

	clone := transport.Clone()
	originalDial := clone.DialContext
	if originalDial == nil {
		dialer := &net.Dialer{}
		originalDial = dialer.DialContext
	}
	secure := &thumbnailTransport{base: clone, transport: clone, closeIdle: true}
	clone.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		resolved, _ := ctx.Value(thumbnailResolvedIPsKey{}).(*thumbnailResolvedIPs)
		if resolved == nil {
			return originalDial(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(normalizedThumbnailHostname(host), resolved.host) {
			// A proxy transport may dial the proxy rather than the thumbnail host.
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

func (t *thumbnailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resolved, err := resolveThumbnailIPs(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	if t.transport != nil {
		ctx := context.WithValue(req.Context(), thumbnailResolvedIPsKey{}, resolved)
		req = req.WithContext(ctx)
	}
	return t.base.RoundTrip(req)
}

func (t *thumbnailTransport) CloseIdleConnections() {
	if t.closeIdle && t.transport != nil {
		t.transport.CloseIdleConnections()
	}
}

func resolveThumbnailIPs(ctx context.Context, u *url.URL) (*thumbnailResolvedIPs, error) {
	if !validThumbnailURL(u) {
		return nil, errors.New("invalid thumbnail destination")
	}
	host := normalizedThumbnailHostname(u.Hostname())
	if ip := parseThumbnailIP(host); ip != nil {
		return &thumbnailResolvedIPs{host: host, ips: []net.IP{ip}}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, errors.New("thumbnail host resolution failed")
	}
	if len(ips) == 0 {
		return nil, errors.New("thumbnail host has no addresses")
	}
	for _, ip := range ips {
		if isBlockedThumbnailIP(ip) {
			return nil, errors.New("thumbnail destination is not public")
		}
	}
	return &thumbnailResolvedIPs{host: host, ips: ips}, nil
}

func normalizedThumbnailHostname(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return host
}

func parseThumbnailIP(host string) net.IP {
	return net.ParseIP(host)
}

func isBlockedThumbnailHostname(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "local" || strings.HasSuffix(host, ".local") ||
		host == "home.arpa" || strings.HasSuffix(host, ".home.arpa")
}

var thumbnailBlockedNetworks = func() []*net.IPNet {
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

func isBlockedThumbnailIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return true
	}
	for _, network := range thumbnailBlockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
