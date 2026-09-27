package memory

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// 面板模块由能力自己的路由提供：模块跟着插件走，停用能力时它才和面板一起消失。
// 宿主只拿到入口地址，从不持有这份代码。
func TestThePanelModuleIsServedByTheCapability(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), ".runtime"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := serveRoute(t, routeFor(t, p, PanelEntryPath), http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("content-type=%q, want a JavaScript type", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"export function mount", "export function unmount", "/api/memory"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the served module does not contain %q", want)
		}
	}
}

// 描述符里的面板与实现一致：声明了就一定暴露（注册表双向校验），入口落在能力
// 自己认领的路由前缀下。
func TestThePanelContributionMatchesTheImplementation(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), ".runtime"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	panels := p.Panels()
	if len(panels) != 1 {
		t.Fatalf("panels=%+v, want exactly one", panels)
	}
	if panels[0].ID != PanelID || panels[0].Title == "" || panels[0].Entry != PanelEntryPath {
		t.Fatalf("panel=%+v", panels[0])
	}
	if !strings.HasPrefix(panels[0].Entry, MemoryRoutePath) {
		t.Fatalf("the panel entry %q must sit under the claimed prefix %q", panels[0].Entry, MemoryRoutePath)
	}
}
