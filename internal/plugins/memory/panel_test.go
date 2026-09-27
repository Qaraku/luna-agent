package memory

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

// 面板样式表同样由能力自己的路由提供，而且真的能拿到：服务的 CSP 是
// default-src 'self'，注入的 <style> 元素会被拒绝，同源的样式表不会。这条路由
// 没送出去，真实服务下面板就是无样式的。
func TestThePanelStylesheetIsServedByTheCapability(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), ".runtime"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := serveRoute(t, routeFor(t, p, PanelStylePath), http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("content-type=%q, want a CSS type", ct)
	}
	body := w.Body.String()
	if strings.TrimSpace(body) == "" {
		t.Fatal("the served stylesheet is empty")
	}
	// 模块写的每个 memory-panel-* 类都要有规则。名单从模块本身解析出来而不是手抄：
	// 手抄的清单会在下次加类时漏掉，而样式表没覆盖的类不会报错，只会静默退回浏览器
	// 默认样式。
	classes := map[string]bool{}
	for _, class := range regexp.MustCompile(`memory-panel-[a-z-]+`).FindAllString(string(panelModule), -1) {
		classes[class] = true
	}
	if len(classes) == 0 {
		t.Fatal("the panel module builds no panel classes at all")
	}
	for class := range classes {
		if !strings.Contains(body, "."+class) {
			t.Fatalf("the served stylesheet has no rule for %q, which the module builds", class)
		}
	}
	// The CSP allows this asset, not a second fetch of something else: a rule
	// that pulled in a remote sheet would be refused in a real service.
	for _, forbidden := range []string{"@import", "http://", "https://"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the stylesheet names %q, which the strict CSP would refuse", forbidden)
		}
	}
}

// 这次缺陷的形状：模块自己注入 <style>，在 default-src 'self' 下被拒绝。模块只能
// 引用能力路由上的样式表，并在卸载时把它摘掉，不能退回内联样式。
func TestThePanelModuleLinksItsStylesheetAndNeverInjectsAStyleElement(t *testing.T) {
	module := string(panelModule)
	for _, forbidden := range []string{"createElement('style')", `createElement("style")`, "style.textContent", "style.cssText", "innerHTML"} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("the panel module still writes inline styles (%q), which the CSP refuses", forbidden)
		}
	}
	// The href is derived from the module's own URL, so module and stylesheet
	// cannot drift apart.
	for _, want := range []string{"new URL('panel.css', import.meta.url)", "link.rel = 'stylesheet'"} {
		if !strings.Contains(module, want) {
			t.Fatalf("the panel module does not link its stylesheet: %q is missing", want)
		}
	}
	// Removal has to sit in unmount: a link that outlives its mount would keep
	// styling a panel that is closed (or a capability that is disabled).
	_, after, found := strings.Cut(module, "export function unmount")
	if !found {
		t.Fatal("the panel module has no unmount")
	}
	if !strings.Contains(after, "state.link.remove()") {
		t.Fatalf("unmount does not remove the stylesheet link")
	}
}

// 面板是产品界面，不是生命周期日志。模块往宿主日志里写"已挂载"一类事件时，每次
// 打开面板都会在下面多出一行，用户看到的就是一串重复日志；那件事发生在模块自己
// 身上，所以这条断言盯的是模块的源码。
func TestThePanelModuleWritesNoLifecycleLog(t *testing.T) {
	module := string(panelModule)
	for _, forbidden := range []string{"已挂载", "未挂载", "api.log", "console."} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("the panel module still writes %q: that is a lifecycle log, not panel content", forbidden)
		}
	}
}

// 能力自己的样式表引用宿主 token，而宿主样式表是另一份文件：名字写错不会报错，
// 浏览器只会把那条声明丢掉（--luna-radius-6 / --luna-font-12 就是这么静默失效
// 的，圆角与字号一直没生效）。所以逐个核对被引用的 token 真的在 web/style.css 里
// 有定义。
func TestThePanelStylesheetOnlyNamesHostTokensThatExist(t *testing.T) {
	hostCSS, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "style.css"))
	if err != nil {
		t.Fatalf("read the host stylesheet: %v", err)
	}
	name := regexp.MustCompile(`--luna-[a-z0-9-]+`)
	defined := make(map[string]bool)
	for _, decl := range regexp.MustCompile(`--luna-[a-z0-9-]+\s*:`).FindAllString(string(hostCSS), -1) {
		defined[strings.TrimSpace(strings.TrimSuffix(decl, ":"))] = true
	}
	referenced := name.FindAllString(string(panelStylesheet), -1)
	if len(referenced) == 0 {
		t.Fatal("the panel stylesheet names no host tokens at all")
	}
	for _, token := range referenced {
		if !defined[token] {
			t.Fatalf("the panel stylesheet names %q, which web/style.css does not define: the browser drops the declaration silently", token)
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
	if !strings.HasPrefix(PanelStylePath, MemoryRoutePath+"/") {
		t.Fatalf("the panel stylesheet %q must sit under the claimed prefix %q", PanelStylePath, MemoryRoutePath)
	}
}
