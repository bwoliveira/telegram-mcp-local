package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxConcurrentWebhookDials = 8

var (
	errPrivateDestination   = errors.New("private destination requires allow_private_network")
	errForbiddenDestination = errors.New("special destination is not allowed")
)

// ipResolver is the DNS seam used both while registering a webhook and before
// every connection. net.Resolver implements it.
type ipResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// contextDialer is the final network seam. secureDialer only passes numeric,
// policy-checked addresses to it.
type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type privateNetworkContextKey struct{}

// withPrivateNetwork carries a webhook's explicit private-network opt-in to
// http.Transport.DialContext without changing the request URL or TLS hostname.
func withPrivateNetwork(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, privateNetworkContextKey{}, allow)
}

func privateNetworkAllowed(ctx context.Context) bool {
	allow, _ := ctx.Value(privateNetworkContextKey{}).(bool)
	return allow
}

// secureDialer resolves and validates the destination immediately before each
// connection. Its semaphore is independent of Manager locks.
type secureDialer struct {
	resolver ipResolver
	base     contextDialer
	sem      chan struct{}
}

// newSecureTransport returns the transport and its validator. Nil dependencies
// select the production resolver and dialer; tests can inject both interfaces.
func newSecureTransport(resolver ipResolver, base contextDialer) (*http.Transport, *secureDialer) {
	return newSecureTransportWithSemaphore(resolver, base, nil)
}

func newSecureTransportWithSemaphore(resolver ipResolver, base contextDialer, sem chan struct{}) (*http.Transport, *secureDialer) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if base == nil {
		base = &net.Dialer{}
	}
	if sem == nil {
		sem = make(chan struct{}, maxConcurrentWebhookDials)
	}
	d := &secureDialer{
		resolver: resolver,
		base:     base,
		sem:      sem,
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}, d
}

func (d *secureDialer) acquire(ctx context.Context) error {
	select {
	case d.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *secureDialer) release() {
	<-d.sem
}

// checkURL performs the registration-time resolution check. DialContext repeats
// the resolution and policy check, so a later DNS rebinding cannot bypass it.
func (d *secureDialer) checkURL(ctx context.Context, raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("webhook URL: scheme must be http or https")
	}
	if u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("webhook URL: host is required")
	}
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	_, err = d.resolveAndValidate(ctx, u.Hostname(), allowPrivate)
	return err
}

// DialContext validates the complete DNS answer before trying any address. It
// then dials numeric addresses directly, preserving the request host for Host
// headers and TLS SNI in net/http.
func (d *secureDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	// net/http deliberately removes the request deadline before dialing. Bound
	// DNS, semaphore waiting and all address attempts independently as well.
	ctx, cancelDial := context.WithTimeout(ctx, 3*time.Second)
	defer cancelDial()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("webhook destination %q: %w", address, err)
	}
	if err := d.acquire(ctx); err != nil {
		return nil, err
	}
	defer d.release()

	addresses, err := d.resolveAndValidate(ctx, host, privateNetworkAllowed(ctx))
	if err != nil {
		return nil, err
	}

	var dialErrors []error
	for i, ip := range addresses {
		// Share the remaining deadline fairly so a blackholed first address
		// cannot consume the time reserved for every later validated address.
		dialCtx := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok && len(addresses)-i > 1 {
			budget := time.Until(deadline) / time.Duration(len(addresses)-i)
			dialCtx, cancel = context.WithTimeout(ctx, budget)
		}
		conn, dialErr := d.base.DialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
		cancel()
		if dialErr == nil {
			return conn, nil
		}
		dialErrors = append(dialErrors, dialErr)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("webhook destination %q: all validated addresses failed: %w", host, errors.Join(dialErrors...))
}

func (d *secureDialer) resolveAndValidate(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if err := validateIP(literal, allowPrivate); err != nil {
			return nil, fmt.Errorf("webhook destination %q: %w", host, err)
		}
		return []netip.Addr{literal}, nil
	}

	addresses, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve webhook destination %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("resolve webhook destination %q: no addresses", host)
	}

	validated := make([]netip.Addr, len(addresses))
	for i, address := range addresses {
		address = address.Unmap()
		if err := validateIP(address, allowPrivate); err != nil {
			// Reject the entire response before dialing: accepting only its public
			// members would make mixed answers dependent on address order.
			return nil, fmt.Errorf("webhook destination %q resolved to %s: %w", host, address, err)
		}
		validated[i] = address
	}
	return validated, nil
}

var (
	metadataAddresses = map[netip.Addr]struct{}{
		netip.MustParseAddr("169.254.169.254"): {},
		netip.MustParseAddr("169.254.170.2"):   {},
		netip.MustParseAddr("169.254.170.23"):  {},
		netip.MustParseAddr("169.254.0.23"):    {},
		netip.MustParseAddr("100.100.100.200"): {},
		netip.MustParseAddr("168.63.129.16"):   {},
		netip.MustParseAddr("fd00:ec2::254"):   {},
		netip.MustParseAddr("fd00:ec2::23"):    {},
	}
	alwaysBlockedIPv4 = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),       // this network
		netip.MustParsePrefix("100.64.0.0/10"),   // shared address space (CGNAT)
		netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
		netip.MustParsePrefix("192.0.2.0/24"),    // documentation
		netip.MustParsePrefix("192.31.196.0/24"), // AS112-v4
		netip.MustParsePrefix("192.52.193.0/24"), // automatic multicast tunneling
		netip.MustParsePrefix("192.88.99.0/24"),  // deprecated 6to4 relay anycast
		netip.MustParsePrefix("192.175.48.0/24"), // direct delegation AS112
		netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
		netip.MustParsePrefix("198.51.100.0/24"), // documentation
		netip.MustParsePrefix("203.0.113.0/24"),  // documentation
		netip.MustParsePrefix("224.0.0.0/4"),     // multicast
		netip.MustParsePrefix("240.0.0.0/4"),     // reserved and limited broadcast
	}
	alwaysBlockedIPv6 = []netip.Prefix{
		netip.MustParsePrefix("100::/64"),          // discard-only
		netip.MustParsePrefix("2001::/23"),         // IETF protocol assignments
		netip.MustParsePrefix("2001:db8::/32"),     // documentation
		netip.MustParsePrefix("3fff::/20"),         // documentation
		netip.MustParsePrefix("5f00::/16"),         // reserved for SRv6 SIDs
		netip.MustParsePrefix("2620:4f:8000::/48"), // direct delegation AS112
		netip.MustParsePrefix("fec0::/10"),         // deprecated site-local
	}
	nat64WellKnown   = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalUse    = netip.MustParsePrefix("64:ff9b:1::/48")
	ipv4Translatable = netip.MustParsePrefix("::ffff:0:0:0/96")
	sixToFour        = netip.MustParsePrefix("2002::/16")
	teredo           = netip.MustParsePrefix("2001::/32")
	v4Compatible     = netip.MustParsePrefix("::/96")
)

// validateIP applies policy after unmapping IPv4-mapped IPv6 addresses.
func validateIP(address netip.Addr, allowPrivate bool) error {
	if !address.IsValid() {
		return errors.New("invalid IP address")
	}
	address = address.Unmap()
	plain := address.WithZone("")

	if _, blocked := metadataAddresses[plain]; blocked {
		return fmt.Errorf("%w: cloud metadata address", errForbiddenDestination)
	}
	if plain.IsUnspecified() || plain.IsMulticast() {
		return fmt.Errorf("%w: unspecified or multicast address", errForbiddenDestination)
	}

	if plain.Is4() {
		for _, prefix := range alwaysBlockedIPv4 {
			if prefix.Contains(plain) {
				return fmt.Errorf("%w: IPv4 range %s", errForbiddenDestination, prefix)
			}
		}
	} else {
		if nat64LocalUse.Contains(plain) {
			return fmt.Errorf("%w: local-use NAT64 address", errForbiddenDestination)
		}
		if teredo.Contains(plain) {
			return fmt.Errorf("%w: Teredo address", errForbiddenDestination)
		}
		if nat64WellKnown.Contains(plain) {
			return validateIP(embeddedIPv4(plain, 12), allowPrivate)
		}
		if ipv4Translatable.Contains(plain) {
			return validateIP(embeddedIPv4(plain, 12), allowPrivate)
		}
		if sixToFour.Contains(plain) {
			return validateIP(embeddedIPv4(plain, 2), allowPrivate)
		}
		if v4Compatible.Contains(plain) && !plain.IsLoopback() {
			return fmt.Errorf("%w: IPv4-compatible IPv6 address", errForbiddenDestination)
		}
		for _, prefix := range alwaysBlockedIPv6 {
			if prefix.Contains(plain) {
				return fmt.Errorf("%w: IPv6 range %s", errForbiddenDestination, prefix)
			}
		}
	}

	if plain.IsPrivate() || plain.IsLoopback() || plain.IsLinkLocalUnicast() {
		if allowPrivate {
			return nil
		}
		return fmt.Errorf("%w: private, loopback, or link-local address", errPrivateDestination)
	}
	if !plain.IsGlobalUnicast() {
		return fmt.Errorf("%w: non-global address", errForbiddenDestination)
	}
	return nil
}

func embeddedIPv4(address netip.Addr, offset int) netip.Addr {
	bytes := address.As16()
	return netip.AddrFrom4([4]byte{bytes[offset], bytes[offset+1], bytes[offset+2], bytes[offset+3]})
}
