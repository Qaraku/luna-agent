package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

// fakePreference 是"用户的选择存在哪里"在这个层看到的形状：它只记下被要求写什么、
// 是否应该失败，另外记一笔写入那一刻注册表里这个能力的状态——顺序对不对只能在那里
// 看出来。它不知道能力是什么，也不知道文件在哪。
type fakePreference struct {
	reg *plugin.Registry
	err error

	asked        []string
	stateAtWrite []string
}

func (p *fakePreference) SetEnabled(id string, enabled bool) error {
	p.asked = append(p.asked, fmt.Sprintf("%s=%t", id, enabled))
	if p.reg != nil {
		state := ""
		if entry, ok := p.reg.Entry(id); ok {
			state = string(entry.State)
		}
		p.stateAtWrite = append(p.stateAtWrite, state)
	}
	return p.err
}

// handlerWithPreference 与 handlerWithCapability 同构，只是多接上记录用户选择的
// seam，并把注册表一起交回来，好让测试直接看运行态。
func handlerWithPreference(t *testing.T, c *fakeCapability, pref CapabilityPreference) (http.Handler, *plugin.Registry) {
	t.Helper()
	reg := plugin.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Enable(c.id); err != nil {
		t.Fatalf("enable: %v", err)
	}
	opts := []Option{WithCapabilities(reg)}
	if pref != nil {
		opts = append(opts, WithCapabilityPreference(pref))
	}
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, opts...), reg
}

func registryState(t *testing.T, reg *plugin.Registry, id string) plugin.State {
	t.Helper()
	entry, ok := reg.Entry(id)
	if !ok {
		t.Fatalf("capability %q is not registered", id)
	}
	return entry.State
}

// 选择先写、运行态后改：写入那一刻注册表里这个能力还得是旧状态，否则失败与成功的
// 区别就分不开了。一次停用接一次启用，两次都要写下来。
func TestACapabilityStateChangeWritesTheChoiceBeforeChangingTheRuntime(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	pref := &fakePreference{}
	h, reg := handlerWithPreference(t, c, pref)
	pref.reg = reg

	if w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/disable", true); w.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", w.Code, w.Body.String())
	}
	if want := "notes=false"; len(pref.asked) != 1 || pref.asked[0] != want {
		t.Fatalf("asked=%v, want [%s]", pref.asked, want)
	}
	if len(pref.stateAtWrite) != 1 || pref.stateAtWrite[0] != string(plugin.StateEnabled) {
		t.Fatalf("state at write=%v, want the runtime still enabled when the choice was written", pref.stateAtWrite)
	}
	if state := registryState(t, reg, c.id); state != plugin.StateDisabled {
		t.Fatalf("state=%s after disabling, want disabled", state)
	}

	if w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/enable", true); w.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", w.Code, w.Body.String())
	}
	wantAsked := []string{"notes=false", "notes=true"}
	if len(pref.asked) != 2 || pref.asked[0] != wantAsked[0] || pref.asked[1] != wantAsked[1] {
		t.Fatalf("asked=%v, want %v", pref.asked, wantAsked)
	}
	if len(pref.stateAtWrite) != 2 || pref.stateAtWrite[1] != string(plugin.StateDisabled) {
		t.Fatalf("state at write=%v, want the runtime still disabled when the choice was written", pref.stateAtWrite)
	}
	if state := registryState(t, reg, c.id); state != plugin.StateEnabled {
		t.Fatalf("state=%s after enabling, want enabled", state)
	}
}

// 写选择失败就是这次请求失败：500，而且运行态一个字节不改——能力仍在服务，它的路由
// 照常应答。
func TestAFailedWriteLeavesTheRuntimeUntouched(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	pref := &fakePreference{err: errors.New("write settings.yaml: permission denied")}
	h, reg := handlerWithPreference(t, c, pref)
	pref.reg = reg

	w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/disable", true)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body=%s, want the reason", w.Body.String())
	}
	if len(pref.asked) != 1 || pref.asked[0] != "notes=false" {
		t.Fatalf("asked=%v, want exactly one call recording the choice", pref.asked)
	}
	if state := registryState(t, reg, c.id); state != plugin.StateEnabled {
		t.Fatalf("state=%s, want the runtime untouched", state)
	}
	if w := capabilityRequest(t, h, http.MethodGet, "/api/notes", false); w.Code != http.StatusOK {
		// 停用没有发生：能力仍在服务，它的路由照常应答。
		t.Fatalf("status=%d, want the capability still serving (body=%s)", w.Code, w.Body.String())
	}
}

// 不提供 seam 时行为与它出现之前完全一致：没有存储可写，启停照旧改运行态。
func TestWithoutAPreferenceTheStateChangeIsUnchanged(t *testing.T) {
	c := &fakeCapability{id: "notes"}
	h, reg := handlerWithPreference(t, c, nil)

	if w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/disable", true); w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if state := registryState(t, reg, c.id); state != plugin.StateDisabled {
		t.Fatalf("state=%s, want disabled", state)
	}
}

// 不认识的能力名由注册表回答，答案仍是 409，不是 404：这不是存储的问题，存储只是
// 把用户这次的要求记了下来。"谁有资格回答"不能在接上存储之后改变。
func TestAnUnknownCapabilityNameIsNotAnAnswerAboutTheStore(t *testing.T) {
	c := &fakeCapability{id: "notes"}
	pref := &fakePreference{}
	h, _ := handlerWithPreference(t, c, pref)

	w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/ghost/disable", true)
	if w.Code == http.StatusNotFound {
		t.Fatalf("status=404, want the registry's own answer (body=%s)", w.Body.String())
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (body=%s)", w.Code, w.Body.String())
	}
	if len(pref.asked) != 1 || pref.asked[0] != "ghost=false" {
		t.Fatalf("asked=%v, want the choice recorded once", pref.asked)
	}
}
