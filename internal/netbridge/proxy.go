package netbridge

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
)

// Proxy 运行在命令的网络命名空间内。唯一出站通道是受宿主约束的 Client；没有直接拨号回退。
type Proxy struct {
	client    *Client
	transport *http.Transport
	forward   *httputil.ReverseProxy
	mu        sync.Mutex
	tunnels   map[net.Conn]net.Conn
}

func NewProxy(client *Client) *Proxy {
	p := &Proxy{client: client, transport: &http.Transport{DialContext: client.DialContext, MaxIdleConns: MaxConnections, MaxIdleConnsPerHost: 4, ForceAttemptHTTP2: false}, tunnels: make(map[net.Conn]net.Conn)}
	p.forward = &httputil.ReverseProxy{Transport: p.transport, Rewrite: func(r *httputil.ProxyRequest) {
		r.Out.URL = r.In.URL
		r.Out.Host = r.In.Host
		r.Out.Header.Del("Proxy-Authorization")
	}, ErrorLog: log.New(io.Discard, "", 0), ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "public network request refused or unavailable: "+err.Error(), http.StatusBadGateway)
	}}
	return p
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if !r.URL.IsAbs() || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.Host == "" {
		http.Error(w, "only absolute HTTP(S) proxy requests are supported", 400)
		return
	}
	p.forward.ServeHTTP(w, r)
}
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	remote, err := p.client.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "public network connection refused: "+err.Error(), 502)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		remote.Close()
		http.Error(w, "proxy transport unavailable", 500)
		return
	}
	local, buffered, err := hijacker.Hijack()
	if err != nil {
		remote.Close()
		return
	}
	p.mu.Lock()
	p.tunnels[local] = remote
	p.mu.Unlock()
	defer func() { local.Close(); remote.Close(); p.mu.Lock(); delete(p.tunnels, local); p.mu.Unlock() }()
	if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if buffered.Flush() != nil {
		return
	}
	done := make(chan struct{})
	go func() { io.Copy(remote, buffered); remote.Close(); close(done) }()
	io.Copy(local, remote)
	local.Close()
	remote.Close()
	<-done
}
func (p *Proxy) Close() {
	p.transport.CloseIdleConnections()
	p.mu.Lock()
	for local, remote := range p.tunnels {
		local.Close()
		remote.Close()
	}
	p.mu.Unlock()
}
