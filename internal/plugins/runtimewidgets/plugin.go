// Package runtimewidgets 提供模型可操作的结构化运行组件，不接收代码、任意模块地址或权限变更。
package runtimewidgets

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/store"
	jsonschema "github.com/eino-contrib/jsonschema"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const PluginID = "runtime-widgets"
const ToolName = "luna_runtime_widget"
const prefix = "/api/runtime-widgets"
const SourcePath = prefix + "/instances"
const modulePath = prefix + "/widget.js"
const stylePath = prefix + "/widget.css"
const maxPerSession = 8
const maxTotal = 128

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

type Metric struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type Content struct {
	Kind     string   `json:"kind"`
	Text     string   `json:"text,omitempty"`
	Progress *float64 `json:"progress,omitempty"`
	Metrics  []Metric `json:"metrics,omitempty"`
	Origin   string   `json:"origin"`
}
type Layout struct {
	Placement string  `json:"placement"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     int     `json:"width"`
	Visible   *bool   `json:"visible,omitempty"`
}
type Instance struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Data   Content `json:"data"`
	Layout Layout  `json:"layout"`
}
type layoutChange struct {
	Placement *string  `json:"placement,omitempty"`
	X         *float64 `json:"x,omitempty"`
	Y         *float64 `json:"y,omitempty"`
	Width     *int     `json:"width,omitempty"`
	Visible   *bool    `json:"visible,omitempty"`
}
type request struct {
	Action   string        `json:"action" jsonschema_description:"upsert, remove or list; operations apply only to the current session"`
	ID       string        `json:"id,omitempty" jsonschema_description:"Stable lowercase slug, required for upsert/remove; at most 48 characters"`
	Title    *string       `json:"title,omitempty" jsonschema_description:"Required for a new widget; at most 80 characters"`
	Kind     *string       `json:"kind,omitempty" jsonschema_description:"status, progress or metrics; defaults to status for a new widget"`
	Text     *string       `json:"text,omitempty" jsonschema_description:"Plain text only, at most 4096 UTF-8 bytes; no HTML or scripts"`
	Progress *float64      `json:"progress,omitempty" jsonschema_description:"Reported progress 0..100; omit when unknown, never invent a measurement"`
	Metrics  []Metric      `json:"metrics,omitempty" jsonschema_description:"Up to eight label/value text pairs; model-supplied, not system telemetry"`
	Layout   *layoutChange `json:"layout,omitempty" jsonschema_description:"Suggested left/right/bottom/float placement, normalized x/y, width 240/300/360/420 and visibility; user choices always win"`
}
type Plugin struct {
	mu       sync.Mutex
	sessions map[string]map[string]Instance
	count    int
}

func New() *Plugin { return &Plugin{sessions: make(map[string]map[string]Instance)} }
func (*Plugin) Descriptor() plugin.Descriptor {
	return plugin.Descriptor{ID: PluginID, Title: "模型运行组件", Deployment: plugin.DeploymentBuiltin, Contributions: []plugin.Contribution{{Kind: plugin.ContributionTool, ID: ToolName}, {Kind: plugin.ContributionWidget, ID: "runtime-cards"}, {Kind: plugin.ContributionRoute, ID: SourcePath}, {Kind: plugin.ContributionRoute, ID: modulePath}, {Kind: plugin.ContributionRoute, ID: stylePath}}, Claims: []plugin.Claim{{Kind: plugin.ClaimWidget, ID: "runtime-cards"}, {Kind: plugin.ClaimRoutePrefix, ID: prefix}}}
}
func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{widgetTool{p}} }
func (*Plugin) Widgets() []plugin.Widget {
	return []plugin.Widget{{ID: "runtime-cards", Title: "模型运行组件", Entry: modulePath, Source: SourcePath}}
}
func (p *Plugin) Routes() []plugin.Route {
	return []plugin.Route{route{SourcePath, p.instances}, route{modulePath, asset("text/javascript; charset=utf-8", module)}, route{stylePath, asset("text/css; charset=utf-8", style)}}
}

type route struct {
	path  string
	serve http.HandlerFunc
}

func (r route) Method() string                                     { return http.MethodGet }
func (r route) Path() string                                       { return r.path }
func (r route) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.serve(w, req) }
func asset(contentType string, data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Write(data)
	}
}
func (p *Plugin) instances(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if store.ValidateID(id) != nil {
		http.Error(w, "valid session is required", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Instances []Instance `json:"instances"`
	}{p.snapshot(id)})
}
func clone(value Instance) Instance {
	value.Data.Metrics = append([]Metric(nil), value.Data.Metrics...)
	if value.Data.Progress != nil {
		n := *value.Data.Progress
		value.Data.Progress = &n
	}
	if value.Layout.Visible != nil {
		v := *value.Layout.Visible
		value.Layout.Visible = &v
	}
	return value
}
func (p *Plugin) snapshot(id string) []Instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Instance, 0, len(p.sessions[id]))
	for _, value := range p.sessions[id] {
		out = append(out, clone(value))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type widgetTool struct{ p *Plugin }

func (widgetTool) Name() string { return ToolName }
func (widgetTool) Description() string {
	return "Create, update, list or remove small structured runtime widgets in the current session. Supported content is plain status text, explicitly reported progress, or label/value metrics. Widgets are marked as model-supplied, never system telemetry. Do not invent measurements. No code, arbitrary URLs, HTML rendering or permission changes are accepted. Layout and visibility are suggestions: a user's hiding, showing or placement choice takes precedence. Upsert changes only supplied fields; use stable ids. At most 8 widgets per session, 4096 text bytes and 8 metric rows per widget. These widgets are transient in this service process; disabling the capability removes its UI and tools but does not delete its in-memory data."
}
func (widgetTool) Schema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(request{})
	s.Required = []string{"action"}
	return s
}
func (t widgetTool) Invoke(ctx context.Context, arguments string) (string, error) {
	if len(arguments) > 16384 {
		return "", fmt.Errorf("widget arguments exceed 16384 bytes")
	}
	var in request
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", fmt.Errorf("expected exactly one widget request")
	}
	info, ok := plugin.Run(ctx)
	if !ok || store.ValidateID(info.SessionID) != nil {
		return "", fmt.Errorf("widget operation requires the current session identity")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if in.Action == "list" {
		data, _ := json.Marshal(t.p.snapshot(info.SessionID))
		return string(data), nil
	}
	if in.Action != "upsert" && in.Action != "remove" {
		return "", fmt.Errorf("action must be upsert, remove or list")
	}
	if !idPattern.MatchString(in.ID) {
		return "", fmt.Errorf("widget id must be a lowercase slug of at most 48 characters")
	}
	t.p.mu.Lock()
	defer t.p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	items := t.p.sessions[info.SessionID]
	current, exists := items[in.ID]
	if in.Action == "remove" {
		if !exists {
			return "", fmt.Errorf("no widget with this id in the current session")
		}
		delete(items, in.ID)
		t.p.count--
		if len(items) == 0 {
			delete(t.p.sessions, info.SessionID)
		}
		return "widget removed", nil
	}
	if !exists {
		if len(items) >= maxPerSession || t.p.count >= maxTotal {
			return "", fmt.Errorf("widget limit reached (8 per session, 128 total)")
		}
		visible := true
		current = Instance{ID: in.ID, Data: Content{Kind: "status", Origin: "model"}, Layout: Layout{Placement: "right", X: .8, Y: .2, Width: 300, Visible: &visible}}
	}
	next := clone(current)
	if in.Title != nil {
		next.Title = *in.Title
	}
	if strings.TrimSpace(next.Title) == "" || len([]rune(next.Title)) > 80 {
		return "", fmt.Errorf("widget title must contain 1..80 characters")
	}
	if in.Kind != nil {
		next.Data.Kind = *in.Kind
	}
	if next.Data.Kind != "status" && next.Data.Kind != "progress" && next.Data.Kind != "metrics" {
		return "", fmt.Errorf("widget kind must be status, progress or metrics")
	}
	if in.Text != nil {
		next.Data.Text = *in.Text
	}
	if len(next.Data.Text) > 4096 || strings.ContainsRune(next.Data.Text, 0) {
		return "", fmt.Errorf("widget text must be at most 4096 bytes without NUL")
	}
	if in.Progress != nil {
		if math.IsNaN(*in.Progress) || math.IsInf(*in.Progress, 0) || *in.Progress < 0 || *in.Progress > 100 {
			return "", fmt.Errorf("progress must be 0..100")
		}
		next.Data.Progress = in.Progress
	}
	if next.Data.Kind != "progress" {
		next.Data.Progress = nil
	}
	if in.Metrics != nil {
		next.Data.Metrics = in.Metrics
	}
	if len(next.Data.Metrics) > 8 {
		return "", fmt.Errorf("at most 8 metrics are allowed")
	}
	for _, metric := range next.Data.Metrics {
		if strings.TrimSpace(metric.Label) == "" || len(metric.Label) > 80 || len(metric.Value) > 128 {
			return "", fmt.Errorf("metric label/value limits are 80/128 bytes")
		}
	}
	if in.Layout != nil {
		change := in.Layout
		if change.Placement != nil {
			next.Layout.Placement = *change.Placement
		}
		if change.X != nil {
			next.Layout.X = *change.X
		}
		if change.Y != nil {
			next.Layout.Y = *change.Y
		}
		if change.Width != nil {
			next.Layout.Width = *change.Width
		}
		if change.Visible != nil {
			next.Layout.Visible = change.Visible
		}
	}
	if next.Layout.Placement != "left" && next.Layout.Placement != "right" && next.Layout.Placement != "bottom" && next.Layout.Placement != "float" {
		return "", fmt.Errorf("invalid widget placement")
	}
	if next.Layout.X < 0 || next.Layout.X > 1 || next.Layout.Y < 0 || next.Layout.Y > 1 || math.IsNaN(next.Layout.X) || math.IsNaN(next.Layout.Y) {
		return "", fmt.Errorf("widget coordinates must be normalized 0..1")
	}
	if next.Layout.Width != 240 && next.Layout.Width != 300 && next.Layout.Width != 360 && next.Layout.Width != 420 {
		return "", fmt.Errorf("widget width must be 240, 300, 360 or 420")
	}
	if items == nil {
		items = make(map[string]Instance)
		t.p.sessions[info.SessionID] = items
	}
	items[in.ID] = clone(next)
	if !exists {
		t.p.count++
	}
	return "widget updated for the current session; user layout and visibility choices take precedence", nil
}

//go:embed widget.js
var module []byte

//go:embed widget.css
var style []byte
