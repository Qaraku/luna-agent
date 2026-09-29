// Package jsonformat 提供一个可直接体验的 JSON 工具能力，处理工作由子进程完成。
package jsonformat

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const PluginID = "json-format"
const ToolName = "luna_format_json"
const RoutePrefix = "/api/json-format"
const FormatPath = RoutePrefix + "/format"
const PanelPath = RoutePrefix + "/panel.js"
const StylePath = RoutePrefix + "/panel.css"

// Source 属于该能力的部署声明，不是模型参数或浏览器配置。
func Source() pluginhost.ToolSpec {
	return pluginhost.ToolSpec{Tool: ToolName, Dir: "json_format", Lazy: true}
}

type Invoker interface {
	InvokeText(context.Context, string, string, string) (pluginhost.Output, error)
}
type Plugin struct{ host Invoker }

func New(host Invoker) *Plugin { return &Plugin{host: host} }
func (*Plugin) Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID: PluginID, Title: "JSON 工具", Deployment: plugin.DeploymentProcess,
		Contributions: []plugin.Contribution{{Kind: plugin.ContributionTool, ID: ToolName}, {Kind: plugin.ContributionRoute, ID: FormatPath}, {Kind: plugin.ContributionRoute, ID: PanelPath}, {Kind: plugin.ContributionRoute, ID: StylePath}, {Kind: plugin.ContributionPanel, ID: PluginID}},
		Claims:        []plugin.Claim{{Kind: plugin.ClaimRoutePrefix, ID: RoutePrefix}},
		Permissions:   []plugin.Permission{{Kind: plugin.PermissionProcessExec}},
	}
}
func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{formatTool{p: p}} }
func (*Plugin) Panels() []plugin.Panel {
	return []plugin.Panel{{ID: PluginID, Title: "JSON 工具", Entry: PanelPath}}
}
func (p *Plugin) Routes() []plugin.Route {
	return []plugin.Route{
		route{method: http.MethodPost, path: FormatPath, serve: p.formatHTTP},
		route{method: http.MethodGet, path: PanelPath, serve: asset("text/javascript; charset=utf-8", panelModule)},
		route{method: http.MethodGet, path: StylePath, serve: asset("text/css; charset=utf-8", panelStyle)},
	}
}

type request struct {
	Text string `json:"text" jsonschema_description:"One JSON value as text, at most 16384 UTF-8 bytes; numbers and duplicate keys are preserved"`
	Mode string `json:"mode,omitempty" jsonschema_description:"pretty (default) or compact"`
}

func decode(reader io.Reader) (request, error) {
	var in *request
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return request{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return request{}, fmt.Errorf("expected exactly one JSON object")
	}
	if in == nil {
		return request{}, fmt.Errorf("expected a JSON object")
	}
	if in.Mode == "" {
		in.Mode = "pretty"
	}
	if in.Mode != "pretty" && in.Mode != "compact" {
		return request{}, fmt.Errorf("mode must be pretty or compact")
	}
	if in.Text == "" || len(in.Text) > 16384 {
		return request{}, fmt.Errorf("text is required and must not exceed 16384 bytes")
	}
	return *in, nil
}
func (p *Plugin) format(ctx context.Context, in request) (pluginhost.Output, error) {
	if p.host == nil {
		return pluginhost.Output{}, fmt.Errorf("%w: JSON process host is not configured", plugin.ErrUnavailable)
	}
	out, err := p.host.InvokeText(ctx, ToolName, in.Text, in.Mode)
	for _, failure := range []error{pluginhost.ErrUnknownTool, pluginhost.ErrNoActivePlugin, pluginhost.ErrRPCTimeout, pluginhost.ErrRPCCanceled, pluginhost.ErrPluginGone} {
		if errors.Is(err, failure) {
			return out, fmt.Errorf("%w: %v", plugin.ErrUnavailable, err)
		}
	}
	return out, err
}

type formatTool struct{ p *Plugin }

func (formatTool) Name() string { return ToolName }
func (formatTool) Description() string {
	return "Validate and format or compact JSON text in a local tool process. Preserves large numbers, duplicate keys and key order. No file access or network requests. text is limited to 16384 UTF-8 bytes, nesting to 64 levels, and output to 65536 bytes. mode is pretty or compact; invalid JSON is refused rather than repaired."
}
func (formatTool) Schema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(request{})
	s.Required = []string{"text"}
	return s
}
func (t formatTool) Invoke(ctx context.Context, arguments string) (string, error) {
	if len(arguments) > 65536 {
		return "", fmt.Errorf("tool arguments exceed 65536 bytes")
	}
	in, err := decode(strings.NewReader(arguments))
	if err != nil {
		return "", err
	}
	out, err := t.p.format(ctx, in)
	return out.Result, err
}

type route struct {
	method, path string
	serve        http.HandlerFunc
}

func (r route) Method() string                                     { return r.method }
func (r route) Path() string                                       { return r.path }
func (r route) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.serve(w, req) }
func send(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (p *Plugin) formatHTTP(w http.ResponseWriter, r *http.Request) {
	in, err := decode(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil {
		send(w, 400, map[string]string{"error": err.Error()})
		return
	}
	out, err := p.format(r.Context(), in)
	if err != nil {
		status := 400
		if errors.Is(err, plugin.ErrUnavailable) {
			status = 503
		}
		send(w, status, map[string]string{"error": err.Error()})
		return
	}
	send(w, 200, map[string]any{"text": out.Result, "version": out.Version, "generation": out.Generation, "plugin_pid": out.PluginPID})
}
func asset(contentType string, data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

//go:embed panel.js
var panelModule []byte

//go:embed panel.css
var panelStyle []byte

var _ plugin.ToolProvider = (*Plugin)(nil)
var _ plugin.RouteProvider = (*Plugin)(nil)
var _ plugin.PanelProvider = (*Plugin)(nil)
