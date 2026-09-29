package netbridge

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPAndCONNECTStayOnTheControlledTransport(t *testing.T) {
	for _, tlsMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[tlsMode], func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "through-bridge") })
			var upstream *httptest.Server
			if tlsMode {
				upstream = httptest.NewTLSServer(handler)
			} else {
				upstream = httptest.NewServer(handler)
			}
			defer upstream.Close()
			target := strings.TrimPrefix(strings.TrimPrefix(upstream.URL, "http://"), "https://")
			dial := func(ctx context.Context, address string) (net.Conn, error) {
				if !strings.HasPrefix(address, "example.test:") {
					t.Errorf("unexpected target: %s", address)
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", target)
			}
			bridge, _, _ := pairedBridge(t, dial)
			proxy := NewProxy(bridge)
			defer proxy.Close()
			server := httptest.NewServer(proxy)
			defer server.Close()
			proxyURL, _ := url.Parse(server.URL)
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			scheme := "http"
			if tlsMode {
				scheme = "https"
			}
			response, err := client.Get(scheme + "://example.test/resource")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "through-bridge" {
				t.Fatalf("body=%q err=%v", body, err)
			}
		})
	}
}
func TestProxyCannotReachLunaControlLoopback(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()
	bridge, _, _ := pairedBridge(t, nil)
	proxy := NewProxy(bridge)
	defer proxy.Close()
	server := httptest.NewServer(proxy)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(upstream.URL + "/api/sessions/forged/execution")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 502 || called {
		t.Fatalf("control endpoint reached: status=%d called=%v", response.StatusCode, called)
	}
}
