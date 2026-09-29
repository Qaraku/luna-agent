package jsonformat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/httpapi"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
)

func buildRoot(t *testing.T) string {
	t.Helper()
	project, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(project, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(project, "internal"), filepath.Join(root, "internal")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "plugins", "json_format")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(project, "plugins", "json_format", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func call(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:43210"+path, strings.NewReader(string(raw)))
	req.Header.Set("Origin", "http://127.0.0.1:43210")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}
func TestJSONCapabilityRunsInAProcessAndHotReloadsRealSource(t *testing.T) {
	root := buildRoot(t)
	host, err := pluginhost.New(context.Background(), root, pluginhost.Options{Tools: []pluginhost.ToolSpec{Source()}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(host.State().Plugins) != 0 {
		t.Fatal("optional process was started before use")
	}
	cap := New(host)
	reg := plugin.NewRegistry(plugin.PermissionProcessExec)
	if err := reg.Register(cap); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(host, nil, sessions, httpapi.Info{BoundHost: "127.0.0.1:43210"}, httpapi.WithCapabilities(reg))
	body := map[string]string{"text": "{\"n\":900719925474099312345}", "mode": "pretty"}
	if r := call(t, handler, http.MethodPost, FormatPath, body); r.Code != 404 {
		t.Fatalf("disabled route=%d", r.Code)
	}
	if r := call(t, handler, http.MethodPost, "/api/plugins/"+PluginID+"/enable", nil); r.Code != 200 {
		t.Fatalf("enable=%d %s", r.Code, r.Body)
	}
	if len(reg.Enabled()) != 1 || reg.Enabled()[0].Descriptor.Deployment != plugin.DeploymentProcess {
		t.Fatal("process capability did not enable")
	}
	if r := call(t, handler, http.MethodGet, PanelPath, nil); r.Code != 200 || !strings.Contains(r.Body.String(), "export function mount") {
		t.Fatal("capability panel not served")
	}
	if r := call(t, handler, http.MethodGet, StylePath, nil); r.Code != 200 {
		t.Fatal("capability style not served")
	}
	response := call(t, handler, http.MethodPost, FormatPath, body)
	if response.Code != 200 {
		t.Fatalf("format=%d %s", response.Code, response.Body)
	}
	var first struct {
		Text       string
		Version    string
		Generation uint64
		PID        int `json:"plugin_pid"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.PID == 0 || first.PID == os.Getpid() || first.Generation == 0 || !strings.Contains(first.Text, "\n  \"n\"") {
		t.Fatalf("first=%+v", first)
	}
	arguments, _ := json.Marshal(body)
	text, err := cap.Tools()[0].Invoke(context.Background(), string(arguments))
	if err != nil || text != first.Text {
		t.Fatalf("model tool and panel differ: %q %v", text, err)
	}
	source := filepath.Join(root, "plugins", "json_format", "main.go")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(data), "const prettyIndent = \"  \"", "const prettyIndent = \"    \"", 1)
	if modified == string(data) {
		t.Fatal("real source was not modified")
	}
	if err := os.WriteFile(source, []byte(modified), 0600); err != nil {
		t.Fatal(err)
	}
	if err := host.Reload(context.Background(), ToolName); err != nil {
		t.Fatal(err)
	}
	after, err := host.InvokeText(context.Background(), ToolName, body["text"], "pretty")
	if err != nil || !strings.Contains(after.Result, "\n    \"n\"") || after.PluginPID == first.PID || after.Generation == first.Generation {
		t.Fatalf("reload=%+v %v", after, err)
	}
	if err := os.WriteFile(source, []byte("package main\ninvalid source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := host.Reload(context.Background(), ToolName); err == nil {
		t.Fatal("bad source reloaded")
	}
	still, err := host.InvokeText(context.Background(), ToolName, body["text"], "pretty")
	if err != nil || still.Result != after.Result || still.PluginPID != after.PluginPID {
		t.Fatalf("failed reload disrupted previous implementation: %+v %v", still, err)
	}
	if r := call(t, handler, http.MethodPost, "/api/plugins/"+PluginID+"/disable", nil); r.Code != 200 {
		t.Fatal(r.Body)
	}
	if len(reg.Enabled()) != 0 {
		t.Fatal("disabled tool remains in the enabled contributions")
	}
	for _, path := range []string{PanelPath, StylePath, FormatPath} {
		method := http.MethodGet
		if path == FormatPath {
			method = http.MethodPost
		}
		if r := call(t, handler, method, path, body); r.Code != 404 {
			t.Fatalf("disabled route %s=%d", path, r.Code)
		}
	}
}

func TestInvalidJSONIsARefusalAndUnavailableProcessIsInfrastructure(t *testing.T) {
	root := buildRoot(t)
	host, err := pluginhost.New(context.Background(), root, pluginhost.Options{Tools: []pluginhost.ToolSpec{Source()}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	tool := New(host).Tools()[0]
	_, err = tool.Invoke(context.Background(), "{\"text\":\"not JSON\"}")
	if err == nil || errors.Is(err, plugin.ErrUnavailable) {
		t.Fatalf("bad JSON classification=%v", err)
	}
	if host.State().Active(ToolName) == nil {
		t.Fatal("invalid input killed the plugin")
	}
	host.Close()
	_, err = tool.Invoke(context.Background(), "{\"text\":\"{}\"}")
	if !errors.Is(err, plugin.ErrUnavailable) {
		t.Fatalf("closed process classification=%v", err)
	}
	if err := plugin.NewRegistry().Register(New(nil)); err == nil {
		t.Fatal("process capability registered without exec permission")
	}
}
