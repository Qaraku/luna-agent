package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"luna-plugin-demo/internal/kernel"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("bind must be a literal loopback IP")
	}
	return net.Listen("tcp", addr)
}
func send(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, err error) {
	send(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if err != nil {
		fail(w, 413, fmt.Errorf("request body exceeds 32768 bytes"))
		return false
	}
	// A single strict JSON object only; no trailing object or unknown fields.
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, err)
		return false
	}
	var extra interface{}
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, fmt.Errorf("expected exactly one JSON object"))
		return false
	}
	return true
}
func New(k *kernel.Kernel, host, web string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != host {
			fail(w, 403, fmt.Errorf("Host must match bound address %s", host))
			return
		}
		if origin, ok := r.Header["Origin"]; ok && (len(origin) != 1 || origin[0] != "http://"+host) {
			fail(w, 403, fmt.Errorf("foreign Origin rejected"))
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, 403, fmt.Errorf("cross-site request rejected"))
			return
		}
		required := "GET"
		if r.URL.Path == "/api/invoke" || r.URL.Path == "/api/reload" {
			required = "POST"
		}
		if r.Method != required {
			w.Header().Set("Allow", required)
			fail(w, 405, fmt.Errorf("method must be %s", required))
			return
		}
		switch r.URL.Path {
		case "/healthz":
			send(w, 200, map[string]bool{"ok": true})
		case "/api/state":
			send(w, 200, k.State())
		case "/api/invoke":
			var in kernel.Input
			if !decode(w, r, &in) {
				return
			}
			if in.DelayMS < 0 || in.DelayMS > 3000 || len(in.Text) > 16384 {
				fail(w, 400, fmt.Errorf("text max 16384 bytes; delay_ms must be 0..3000"))
				return
			}
			// Ignore HTTP cancellation: retain the reference until the actual RPC returns.
			out, err := k.Invoke(in)
			if err != nil {
				fail(w, 502, err)
				return
			}
			send(w, 200, out)
		case "/api/reload":
			var in struct {
				Candidate string `json:"candidate"`
			}
			if !decode(w, r, &in) {
				return
			}
			if in.Candidate != "v1" && in.Candidate != "v2" && in.Candidate != "broken" {
				fail(w, 400, fmt.Errorf("candidate must be v1, v2 or broken"))
				return
			}
			if err := k.Reload(r.Context(), in.Candidate); err != nil {
				fail(w, 409, err)
				return
			}
			send(w, 200, k.State())
		case "/", "/app.js", "/style.css":
			name := r.URL.Path[1:]
			if name == "" {
				name = "index.html"
			}
			path := filepath.Join(web, name)
			if info, err := os.Stat(path); err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, path)
		default:
			http.NotFound(w, r)
		}
	})
}
