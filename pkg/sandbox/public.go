package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

func SensitiveAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() || sharedAddressSpace.Contains(a) ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast()
}

var ErrLocalAddress = errors.New("resolves to a local or private address")

// PublicTransport keeps requests off the network this process is on.
func PublicTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		// No proxy from the environment: a proxy would make the dial below check the proxy instead of the site.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialPublic(ctx, dialer, network, addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}

// dialPublic connects only to public addresses. It checks the addresses the name resolves to at dial time,
// so a name cannot point at the local network after it was approved.
func dialPublic(ctx context.Context, dialer *net.Dialer, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var lastErr error = fmt.Errorf("sandbox: %s has no address", host)
	for _, a := range addrs {
		if SensitiveAddr(a) {
			lastErr = fmt.Errorf("sandbox: %s %w", host, ErrLocalAddress)
			continue
		}
		c, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
