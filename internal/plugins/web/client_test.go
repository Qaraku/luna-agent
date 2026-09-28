package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testFetchTimeout is the time limit the end-to-end tests give one fetch: the product's
// twenty seconds would only make the suite slow, and the limit that matters here is that a
// fetch which does not finish is refused instead of reported.
const testFetchTimeout = 2 * time.Second

// allowEveryAddress lets everything through. It exists only in tests: it is what lets an
// httptest server on 127.0.0.1 be fetched at all, so that everything else about a call can
// be tested against a real exchange.
func allowEveryAddress(net.IP) error { return nil }

// noProxy is a proxy function that says no request goes through a proxy. The test seam
// needs it because the two layers must agree: the transport uses this function to route,
// and the tool uses it to decide whether the dial-layer check applies.
func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

// loopbackOnly is a policy that admits loopback and judges everything else the way the
// product does. Some tests need a first hop the dial layer will accept and a target it
// will not.
func loopbackOnly(ip net.IP) error {
	if ip.IsLoopback() {
		return nil
	}
	return publicAddresses(ip)
}

// The address policy is one function, and these are its cases: every address that is not
// public is refused and the refusal names what makes it not public, and a public address
// passes. IPv4 and IPv6 are the same list on purpose — a policy that only guards one of
// them guards half of the addresses this machine can reach.
func TestTheAddressPolicyRefusesEveryAddressThatIsNotPublic(t *testing.T) {
	cases := []struct {
		name string
		addr string
		// want is the reason the refusal has to name; an empty want means the address is
		// public and must be admitted.
		want string
	}{
		{"an ipv4 loopback address", "127.0.0.1", "loopback"},
		{"an ipv4 loopback address that is not the first", "127.9.9.9", "loopback"},
		{"an ipv6 loopback address", "::1", "loopback"},
		{"an ipv4 address written as ipv6", "::ffff:127.0.0.1", "loopback"},
		{"a private address at the bottom of 10/8", "10.0.0.1", "private"},
		{"a private address in the middle of 172.16/12", "172.20.5.4", "private"},
		{"a private address at the top of 192.168/16", "192.168.1.1", "private"},
		{"an ipv6 unique local address", "fc00::1", "private"},
		{"an ipv6 unique local address with a random prefix", "fd12:3456:789a::1", "private"},
		{"an ipv4 link-local address", "169.254.1.1", "link-local"},
		{"an ivp6 link-local address", "fe80::1", "link-local"},
		{"the ipv4 unspecified address", "0.0.0.0", "unspecified"},
		{"the ipv6 unspecified address", "::", "unspecified"},
		{"an ipv4 multicast address", "224.0.0.1", "multicast"},
		{"an ipv4 multicast address that is one of the best known", "239.255.255.250", "multicast"},
		{"an ipv6 multicast address", "ff02::1", "multicast"},
		{"a 6to4 address carrying a loopback address", "2002:7f00:1::1", "6to4"},
		{"a 6to4 address carrying a private address", "2002:0a00:1::1", "6to4"},
		{"a nat64 address carrying a private address", "64:ff9b::10.0.0.1", "NAT64"},
		{"a nat64 address carrying a loopback address", "64:ff9b::7f00:1", "NAT64"},
		{"the address a public resolver answers on", "1.1.1.1", ""},
		{"a public address that is not special in any way", "93.184.216.34", ""},
		{"a public ipv6 address", "2606:4700:4700::1111", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.addr)
			if ip == nil {
				t.Fatalf("the case's own address %q does not parse", tc.addr)
			}
			err := publicAddresses(ip)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("a public address was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was allowed by the address policy", tc.addr)
			}
			// The refusal names the address in its canonical form, which is what net.IP
			// prints: an IPv4 address written as IPv6 comes back as the IPv4 one.
			for _, want := range []string{tc.want, ip.String(), "only public addresses"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not name %q", err, want)
				}
			}
		})
	}
}

// The layer before the request: an address written into the url is judged directly, a name
// is resolved first and every address it resolves to has to pass — one bad address is
// enough to refuse the whole host.
func TestCheckHostRefusesAnAddressItCannotReachAndAdmitsOneItCan(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"a loopback literal", "127.0.0.1", "loopback"},
		{"a loopback literal in brackets", "[::1]", "loopback"},
		{"a private literal", "10.1.2.3", "private"},
		{"an unspecified literal", "0.0.0.0", "unspecified"},
		{"a public literal", "93.184.216.34", ""},
		{"a name that resolves to loopback", "localhost", "loopback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkHost(context.Background(), publicAddresses, tc.host)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("%q was refused: %v", tc.host, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q was allowed by the host check", tc.host)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q does not name %q", err, tc.want)
			}
		})
	}
}

// A name that cannot be resolved is refused: a host this machine cannot place is not a
// host it may send a request to. The case is skipped when the resolver of this machine
// answers for anything at all, because then there is nothing here to pin.
func TestCheckHostRefusesANameThatDoesNotResolve(t *testing.T) {
	const host = "luna-agent-does-not-exist.invalid"
	if addrs, err := net.LookupHost(host); err == nil {
		t.Skipf("this resolver answers for anything, including %s (%v)", host, addrs)
	}
	err := checkHost(context.Background(), publicAddresses, host)
	if err == nil {
		t.Fatalf("%q was allowed", host)
	}
	if !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("refusal %q does not say the name did not resolve", err)
	}
}

// The layer at the connection: what Control sees is the address that was actually
// resolved, so a name that looked fine when it was checked is judged again here.
func TestCheckDialAddressJudgesTheResolvedAddress(t *testing.T) {
	cases := []struct {
		name    string
		address string
		want    string
	}{
		{"a loopback address", "127.0.0.1:8080", "loopback"},
		{"a private address", "10.0.0.1:80", "private"},
		{"an ipv6 loopback address", "[::1]:80", "loopback"},
		{"an ipv6 unique local address", "[fd00::1]:443", "private"},
		{"a link-local address", "169.254.169.254:80", "link-local"},
		{"the metadata address is not spared", "169.254.169.254:80", "link-local"},
		{"a public address", "93.184.216.34:443", ""},
		{"an address that is not an ip at all", "example.com:80", "is not an IP address"},
		{"an address that is not host:port", "93.184.216.34", "cannot be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDialAddress(publicAddresses, tc.address)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("%s was refused: %v", tc.address, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was allowed at the dial layer", tc.address)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q does not name %q", err, tc.want)
			}
		})
	}
}

// The product's own rules refuse a loopback address before any request is made, and this
// is the tool the product builds — not a test-configured one.
func TestTheProductRulesRefuseLoopbackBeforeAnyRequestIsMade(t *testing.T) {
	touched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		touched = true
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "reached")
	}))
	t.Cleanup(server.Close)

	tool := NewFetchTool()
	for _, target := range []string{server.URL + "/page", "http://localhost:1/page", "http://10.0.0.1/page", "http://[::1]:1/page"} {
		got, err := tool.Invoke(context.Background(), args(t, map[string]any{"url": target}))
		refused(t, got, err, "only public addresses")
	}
	if touched {
		t.Fatal("a request reached the loopback server, so the address policy did not run first")
	}
}

// A proxy changes what the dial layer sees: the address being dialled is the proxy's
// (usually 127.0.0.1), not the target's, so the check there is skipped — and it has to be,
// or every fetch through a proxy would be refused as a loopback request. The hostname
// layer still runs, and this test pins that the request really did go to the proxy.
func TestTheDialCheckStepsAsideWhenTheRequestGoesThroughAProxy(t *testing.T) {
	var proxied string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = r.URL.String() + " host=" + r.Host
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<title>Through the proxy</title><p>the body</p>")
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse the proxy address: %v", err)
	}

	// 192.0.2.10 is TEST-NET-1: public as far as this tool's policy is concerned, and it is
	// never dialled by this process — the proxy is the one that connects.
	const target = "http://192.0.2.10/page"
	rules := fetchRules{
		addresses: publicAddresses,
		// The dial layer judges loopback, and the proxy is on loopback: this is the
		// policy that would refuse the proxy address if the check were not skipped.
		dial:    publicAddresses,
		proxy:   func(*http.Request) (*url.URL, error) { return proxyURL, nil },
		timeout: testFetchTimeout,
	}
	tool := newFetchTool(newClient(rules), rules)

	got, err := tool.Invoke(context.Background(), args(t, map[string]any{"url": target}))
	if err != nil {
		t.Fatalf("a fetch through a proxy was refused: %v", err)
	}
	if !strings.Contains(proxied, target) {
		t.Fatalf("the proxy did not receive %q: it saw %q", target, proxied)
	}
	for _, want := range []string{"fetched " + target, "# Through the proxy", "the body"} {
		if !strings.Contains(got, want) {
			t.Errorf("result %q does not carry %q", got, want)
		}
	}
}

// The dial check is not decoration: with no proxy the same kind of tool refuses a loopback
// target at the layer that actually connects, even though the hostname layer was told to
// let everything through. Together with the case above this pins the exception as an
// exception rather than as a check that never runs.
func TestTheDialCheckRefusesALoopbackTargetWhenThereIsNoProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request reached the server, so the dial-layer check did not stop it")
	}))
	t.Cleanup(server.Close)

	rules := fetchRules{
		addresses: allowEveryAddress,
		dial:      publicAddresses,
		proxy:     noProxy,
		timeout:   testFetchTimeout,
	}
	tool := newFetchTool(newClient(rules), rules)
	got, err := tool.Invoke(context.Background(), args(t, map[string]any{"url": server.URL + "/page"}))
	refused(t, got, err, "loopback", "only public addresses")
}

// The dial check also sees a name resolving to something this machine must not reach, which
// is the case the hostname layer can only judge from the outside. The hostname layer is
// told to admit loopback here and the dial layer is not, so the refusal can only have come
// from the connection attempt to 127.0.0.1.
func TestTheDialCheckCatchesANameThatResolvesToLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request reached the server, so the dial-layer check did not stop it")
	}))
	t.Cleanup(server.Close)

	rules := fetchRules{
		addresses: allowEveryAddress,
		dial:      publicAddresses,
		proxy:     noProxy,
		timeout:   testFetchTimeout,
	}
	tool := newFetchTool(newClient(rules), rules)
	// 127.0.0.1 spelled as a name whose resolution the dial layer has to judge.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split the server address: %v", err)
	}
	got, err := tool.Invoke(context.Background(), args(t, map[string]any{"url": "http://localhost:" + port + "/page"}))
	refused(t, got, err, "loopback", "only public addresses")
}

// A cancelled run hands the context's own error back to the kernel, which treats it as the
// end of the round rather than as this call's refusal. It is pinned here rather than in the
// end-to-end tests because the server has to hang for the duration.
func TestFetchReturnsTheContextErrorWhenTheRunIsCancelled(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(blocked) })

	rules := fetchRules{addresses: allowEveryAddress, dial: allowEveryAddress, proxy: noProxy, timeout: 30 * time.Second}
	tool := newFetchTool(newClient(rules), rules)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := tool.Invoke(ctx, args(t, map[string]any{"url": server.URL}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
