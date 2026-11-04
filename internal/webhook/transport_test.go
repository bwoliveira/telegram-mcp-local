package webhook

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func successfulDialer(record func(string)) contextDialer {
	return dialerFunc(func(_ context.Context, _ string, address string) (net.Conn, error) {
		if record != nil {
			record(address)
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	})
}

func TestValidateIPPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		address      string
		allowPrivate bool
		wantAllowed  bool
	}{
		{name: "public IPv4", address: "93.184.216.34", wantAllowed: true},
		{name: "private IPv4", address: "10.0.0.1"},
		{name: "private IPv4 allowed", address: "10.0.0.1", allowPrivate: true, wantAllowed: true},
		{name: "loopback IPv4", address: "127.0.0.1"},
		{name: "loopback IPv4 allowed", address: "127.0.0.1", allowPrivate: true, wantAllowed: true},
		{name: "link-local IPv4", address: "169.254.20.30"},
		{name: "link-local IPv4 allowed", address: "169.254.20.30", allowPrivate: true, wantAllowed: true},
		{name: "AWS metadata", address: "169.254.169.254", allowPrivate: true},
		{name: "AWS ECS metadata", address: "169.254.170.2", allowPrivate: true},
		{name: "AWS EKS Pod Identity metadata", address: "169.254.170.23", allowPrivate: true},
		{name: "Tencent metadata", address: "169.254.0.23", allowPrivate: true},
		{name: "Alibaba metadata", address: "100.100.100.200", allowPrivate: true},
		{name: "Azure platform virtual IP", address: "168.63.129.16", allowPrivate: true},
		{name: "carrier-grade NAT", address: "100.64.0.1", allowPrivate: true},
		{name: "documentation IPv4", address: "192.0.2.1", allowPrivate: true},
		{name: "benchmark IPv4", address: "198.18.0.1", allowPrivate: true},
		{name: "multicast IPv4", address: "224.0.0.1", allowPrivate: true},
		{name: "unspecified IPv4", address: "0.0.0.0", allowPrivate: true},
		{name: "reserved IPv4", address: "240.0.0.1", allowPrivate: true},
		{name: "AS112 IPv4", address: "192.31.196.1", allowPrivate: true},
		{name: "AMT IPv4", address: "192.52.193.1", allowPrivate: true},
		{name: "direct delegation IPv4", address: "192.175.48.1", allowPrivate: true},
		{name: "public IPv6", address: "2606:2800:220:1:248:1893:25c8:1946", wantAllowed: true},
		{name: "public IPv6 with private-looking suffix", address: "2001:4860:4860::a00:1", wantAllowed: true},
		{name: "public IPv6 with metadata-looking suffix", address: "2001:4860:4860::a9fe:a9fe", wantAllowed: true},
		{name: "ULA", address: "fd12:3456::1"},
		{name: "ULA allowed", address: "fd12:3456::1", allowPrivate: true, wantAllowed: true},
		{name: "loopback IPv6", address: "::1"},
		{name: "loopback IPv6 allowed", address: "::1", allowPrivate: true, wantAllowed: true},
		{name: "link-local IPv6", address: "fe80::1"},
		{name: "link-local IPv6 allowed", address: "fe80::1", allowPrivate: true, wantAllowed: true},
		{name: "AWS metadata IPv6", address: "fd00:ec2::254", allowPrivate: true},
		{name: "AWS EKS Pod Identity metadata IPv6", address: "fd00:ec2::23", allowPrivate: true},
		{name: "documentation IPv6", address: "2001:db8::1", allowPrivate: true},
		{name: "benchmark IPv6", address: "2001:2::1", allowPrivate: true},
		{name: "multicast IPv6", address: "ff02::1", allowPrivate: true},
		{name: "unspecified IPv6", address: "::", allowPrivate: true},
		{name: "mapped private IPv4", address: "::ffff:10.0.0.1"},
		{name: "mapped private IPv4 allowed", address: "::ffff:10.0.0.1", allowPrivate: true, wantAllowed: true},
		{name: "mapped metadata IPv4", address: "::ffff:169.254.169.254", allowPrivate: true},
		{name: "IPv4-translatable private IPv4", address: "::ffff:0:10.0.0.1"},
		{name: "IPv4-translatable private IPv4 allowed", address: "::ffff:0:10.0.0.1", allowPrivate: true, wantAllowed: true},
		{name: "NAT64 public IPv4", address: "64:ff9b::5db8:d822", wantAllowed: true},
		{name: "NAT64 private IPv4", address: "64:ff9b::a00:1"},
		{name: "NAT64 private IPv4 allowed", address: "64:ff9b::a00:1", allowPrivate: true, wantAllowed: true},
		{name: "unconfigured network-specific NAT64 private-looking suffix", address: "2001:4860:64:1::a00:1", wantAllowed: true},
		{name: "unconfigured network-specific NAT64 metadata-looking suffix", address: "2001:4860:64:1::a9fe:a9fe", allowPrivate: true, wantAllowed: true},
		{name: "local-use NAT64 prefix", address: "64:ff9b:1::1", allowPrivate: true},
		{name: "6to4 public IPv4", address: "2002:5db8:d822::1", wantAllowed: true},
		{name: "6to4 private IPv4", address: "2002:0a00:0001::1"},
		{name: "6to4 private IPv4 allowed", address: "2002:0a00:0001::1", allowPrivate: true, wantAllowed: true},
		{name: "Teredo", address: "2001:0000:4136:e378:8000:63bf:3fff:fdd2", allowPrivate: true},
		{name: "direct delegation IPv6", address: "2620:4f:8000::1", allowPrivate: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIP(netip.MustParseAddr(tt.address), tt.allowPrivate)
			if got := err == nil; got != tt.wantAllowed {
				t.Fatalf("validateIP(%s, %v) error = %v; allowed = %v, want %v", tt.address, tt.allowPrivate, err, got, tt.wantAllowed)
			}
		})
	}
}

func TestCheckURLRejectsMixedDNSAnswers(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" || host != "receiver.example" {
			t.Fatalf("LookupNetIP(%q, %q)", network, host)
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")}, nil
	})
	_, d := newSecureTransport(resolver, successfulDialer(nil))

	if err := d.checkURL(context.Background(), "https://receiver.example/hook", false); err == nil {
		t.Fatal("checkURL accepted a DNS response containing a private address")
	}
}

func TestCheckURLValidatesSchemeHostAndLiteralAddress(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("an IP literal must not be resolved through DNS")
		return nil, nil
	})
	_, d := newSecureTransport(resolver, successfulDialer(nil))

	for _, raw := range []string{"ftp://example.com/hook", "https:///hook", "http://10.0.0.1/hook"} {
		if err := d.checkURL(context.Background(), raw, false); err == nil {
			t.Errorf("checkURL(%q) unexpectedly succeeded", raw)
		}
	}
	if err := d.checkURL(context.Background(), "http://10.0.0.1/hook", true); err != nil {
		t.Fatalf("private literal with opt-in: %v", err)
	}
}

func TestDialContextBlocksDNSRebindingBeforeBaseDial(t *testing.T) {
	t.Parallel()

	var lookups atomic.Int32
	resolver := resolverFunc(func(_ context.Context, _, _ string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	})
	var baseCalls atomic.Int32
	_, d := newSecureTransport(resolver, dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		baseCalls.Add(1)
		return nil, errors.New("must not be called")
	}))

	if err := d.checkURL(context.Background(), "https://receiver.example/hook", false); err != nil {
		t.Fatalf("registration check failed: %v", err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", "receiver.example:443"); err == nil {
		t.Fatal("DialContext accepted a rebound private address")
	}
	if got := baseCalls.Load(); got != 0 {
		t.Fatalf("base dialer called %d times, want 0", got)
	}
}

func TestEveryRetryDialRevalidatesDNS(t *testing.T) {
	var lookups atomic.Int32
	resolver := resolverFunc(func(_ context.Context, _, _ string) ([]netip.Addr, error) {
		switch lookups.Add(1) {
		case 1, 2: // Registration and the first delivery attempt.
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		default: // A later retry sees a rebound private address.
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		}
	})
	var baseCalls atomic.Int32
	_, d := newSecureTransport(resolver, successfulDialer(func(string) { baseCalls.Add(1) }))

	if err := d.checkURL(context.Background(), "https://receiver.example/hook", false); err != nil {
		t.Fatalf("registration check failed: %v", err)
	}
	conn, err := d.DialContext(context.Background(), "tcp", "receiver.example:443")
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	_ = conn.Close()
	if _, err := d.DialContext(context.Background(), "tcp", "receiver.example:443"); err == nil {
		t.Fatal("retry accepted a rebound private address")
	}
	if got := baseCalls.Load(); got != 1 {
		t.Fatalf("base dialer calls = %d, want only the first public attempt", got)
	}
}

func TestDialContextReservesTimeForLaterAddresses(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("1.1.1.1")}, nil
	})
	var attempts []string
	_, d := newSecureTransport(resolver, dialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		attempts = append(attempts, address)
		if address == "93.184.216.34:443" {
			// Model a blackholed address without relying on external routing.
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return successfulDialer(nil).DialContext(ctx, network, address)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", "receiver.example:443")
	if err != nil {
		t.Fatalf("blackholed first address prevented working fallback: %v (attempts %v)", err, attempts)
	}
	_ = conn.Close()
	if len(attempts) != 2 || attempts[1] != "1.1.1.1:443" {
		t.Fatalf("attempts = %v, want both validated addresses in order", attempts)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("fallback exhausted the overall deadline: %v", err)
	}
}

func TestTransportReservesTimeWithoutRequestDialDeadline(t *testing.T) {
	t.Parallel()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("1.1.1.1")}, nil
	})
	var calls atomic.Int32
	transport, _ := newSecureTransport(resolver, dialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if strings.HasPrefix(address, "93.184.216.34:") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, network, receiver.Listener.Addr().String())
	}))
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("http://receiver.example/hook")
	if err != nil {
		t.Fatalf("HTTP fallback failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent || calls.Load() != 2 {
		t.Fatalf("status=%d attempts=%d", response.StatusCode, calls.Load())
	}
}

func TestDialContextConnectsToValidatedIPInsteadOfHostname(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host != "receiver.example" {
			t.Fatalf("resolved host %q", host)
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	var gotAddress string
	_, d := newSecureTransport(resolver, successfulDialer(func(address string) { gotAddress = address }))

	conn, err := d.DialContext(context.Background(), "tcp", "receiver.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if gotAddress != "93.184.216.34:8443" {
		t.Fatalf("base dial address = %q, want direct validated IP", gotAddress)
	}
}

func TestSecureTransportPreservesOriginalHostAndTLSServerName(t *testing.T) {
	certificate := newTestCertificate(t)
	sni := make(chan string, 1)
	hostHeader := make(chan string, 1)
	serverErr := make(chan error, 1)
	var dialAddress string

	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	base := dialerFunc(func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialAddress = address
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			tlsServer := tls.Server(server, &tls.Config{
				Certificates: []tls.Certificate{certificate},
				GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
					sni <- hello.ServerName
					return nil, nil
				},
			})
			if err := tlsServer.Handshake(); err != nil {
				serverErr <- err
				return
			}
			req, err := http.ReadRequest(bufio.NewReader(tlsServer))
			if err != nil {
				serverErr <- err
				return
			}
			hostHeader <- req.Host
			if _, err := io.WriteString(tlsServer, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err != nil {
				serverErr <- err
				return
			}
			serverErr <- nil
		}()
		return client, nil
	})
	transport, _ := newSecureTransport(resolver, base)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Test certificate only.

	req, err := http.NewRequest(http.MethodPost, "https://receiver.example/hook", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if dialAddress != "93.184.216.34:443" {
		t.Fatalf("base dial address = %q, want validated numeric IP", dialAddress)
	}
	if got := <-sni; got != "receiver.example" {
		t.Fatalf("TLS SNI = %q, want original hostname", got)
	}
	if got := <-hostHeader; got != "receiver.example" {
		t.Fatalf("HTTP Host = %q, want original hostname", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("TLS test server: %v", err)
	}
}

func newTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestDialContextPrivateNetworkOptInComesFromContext(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
	})
	var calls atomic.Int32
	_, d := newSecureTransport(resolver, successfulDialer(func(string) { calls.Add(1) }))

	if _, err := d.DialContext(context.Background(), "tcp", "receiver.example:80"); err == nil {
		t.Fatal("private address succeeded without opt-in")
	}
	conn, err := d.DialContext(withPrivateNetwork(context.Background(), true), "tcp", "receiver.example:80")
	if err != nil {
		t.Fatalf("private address with opt-in: %v", err)
	}
	_ = conn.Close()
	if got := calls.Load(); got != 1 {
		t.Fatalf("base dialer calls = %d, want 1", got)
	}
}

func TestSecureTransportIgnoresProxyEnvironmentAndDisablesKeepAlives(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:3128")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")

	transport, _ := newSecureTransport(nil, nil)
	if transport.Proxy != nil {
		t.Fatal("transport.Proxy must be nil so environment proxies are ignored")
	}
	if !transport.DisableKeepAlives {
		t.Fatal("DisableKeepAlives = false, want true to force a fresh DNS check and dial")
	}
	if transport.DialContext == nil {
		t.Fatal("secure DialContext is not installed")
	}
}

func TestSecureDialerLimitsConcurrencyAndSemaphoreIsCancelable(t *testing.T) {
	const total = 20

	gate := make(chan struct{})
	var active atomic.Int32
	var peak atomic.Int32
	var entered atomic.Int32
	resolver := resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		n := active.Add(1)
		defer active.Add(-1)
		entered.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-gate:
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	_, d := newSecureTransport(resolver, successfulDialer(nil))

	errs := make(chan error, total)
	for range total {
		go func() {
			conn, err := d.DialContext(context.Background(), "tcp", "receiver.example:443")
			if conn != nil {
				_ = conn.Close()
			}
			errs <- err
		}()
	}

	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < maxConcurrentWebhookDials && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := entered.Load(); got != maxConcurrentWebhookDials {
		close(gate)
		t.Fatalf("concurrent operations entered = %d, want %d", got, maxConcurrentWebhookDials)
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, err := d.DialContext(ctx, "tcp", "canceled.example:443")
		canceled <- err
	}()
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			close(gate)
			t.Fatalf("waiting DialContext error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("canceled DialContext remained blocked on the semaphore")
	}

	close(gate)
	for range total {
		if err := <-errs; err != nil {
			t.Errorf("DialContext: %v", err)
		}
	}
	if got := peak.Load(); got > maxConcurrentWebhookDials {
		t.Fatalf("peak concurrent resolutions = %d, limit = %d", got, maxConcurrentWebhookDials)
	}
}

func TestSecureDialerLimitsActualBaseDials(t *testing.T) {
	const total = 20
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	gate := make(chan struct{})
	var active, entered, peak atomic.Int32
	base := dialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		n := active.Add(1)
		defer active.Add(-1)
		entered.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-gate:
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	_, d := newSecureTransport(resolver, base)
	errs := make(chan error, total)
	for range total {
		go func() {
			conn, err := d.DialContext(context.Background(), "tcp", "receiver.example:443")
			if conn != nil {
				_ = conn.Close()
			}
			errs <- err
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < maxConcurrentWebhookDials && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := entered.Load(); got != maxConcurrentWebhookDials {
		close(gate)
		t.Fatalf("base dials entered = %d, want %d before release", got, maxConcurrentWebhookDials)
	}
	close(gate)
	for range total {
		if err := <-errs; err != nil {
			t.Errorf("DialContext: %v", err)
		}
	}
	if got := peak.Load(); got > maxConcurrentWebhookDials {
		t.Fatalf("peak base dials = %d, limit = %d", got, maxConcurrentWebhookDials)
	}
}

func TestDialContextRejectsMixedDNSBeforeDialingAnyAddress(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}, nil
	})
	var calls atomic.Int32
	_, d := newSecureTransport(resolver, successfulDialer(func(string) { calls.Add(1) }))

	if _, err := d.DialContext(withPrivateNetwork(context.Background(), true), "tcp", "receiver.example:443"); err == nil {
		t.Fatal("mixed DNS response unexpectedly succeeded")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("base dialer called %d times, want 0", got)
	}
}

func TestCheckURLResolutionHonorsCancellation(t *testing.T) {
	t.Parallel()

	resolver := resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	_, d := newSecureTransport(resolver, successfulDialer(nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.checkURL(ctx, "https://receiver.example/hook", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("checkURL error = %v, want context.Canceled", err)
	}
}

var _ ipResolver = resolverFunc(nil)
var _ contextDialer = dialerFunc(nil)
