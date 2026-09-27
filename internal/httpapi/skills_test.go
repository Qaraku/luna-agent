package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

// fakeCatalog is the Skills capability as this layer sees it: a list it reports
// and a record of what it was asked to change. Nothing here knows how a skill is
// discovered or where a preference is stored — that is the point of the seam
// under test.
type fakeCatalog struct {
	skills []SkillRef
	asked  []string
	err    error
}

func (c *fakeCatalog) Skills() []SkillRef { return c.skills }

func (c *fakeCatalog) SetState(name string, enabled bool) (SkillRef, bool, error) {
	c.asked = append(c.asked, fmt.Sprintf("%s=%t", name, enabled))
	if c.err != nil {
		return SkillRef{}, false, c.err
	}
	for i, ref := range c.skills {
		if ref.Name != name {
			continue
		}
		ref.Enabled = enabled
		ref.DisabledReason = ""
		if !enabled {
			ref.DisabledReason = "已在设置里停用"
		}
		c.skills[i] = ref
		return ref, true, nil
	}
	return SkillRef{}, false, nil
}

func handlerWithSkills(t *testing.T, catalog SkillCatalog) http.Handler {
	t.Helper()
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithSkills(catalog))
}

func skillRequest(t *testing.T, h http.Handler, method, path string, withOrigin bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = "127.0.0.1:43210"
	if withOrigin {
		req.Header.Set("Origin", "http://127.0.0.1:43210")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// skillList decodes the endpoint's answer into maps, so a test can assert the
// exact key set a client is promised rather than only the fields it happens to
// read.
func skillList(t *testing.T, body string) []map[string]any {
	t.Helper()
	var decoded struct {
		Skills []map[string]any `json:"skills"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body=%s err=%v", body, err)
	}
	return decoded.Skills
}

func skillBody(t *testing.T, body string) map[string]any {
	t.Helper()
	decoded := skillList(t, body)
	if len(decoded) != 1 {
		t.Fatalf("body=%s, want exactly one skill", body)
	}
	return decoded[0]
}

func TestTheSkillsEndpointReportsEverySkillInTheFrozenShape(t *testing.T) {
	catalog := &fakeCatalog{skills: []SkillRef{
		{Name: "demo", Description: "Does a demo.", Scope: "user", Enabled: true},
		{Name: "other", Description: "Does another.", Scope: "builtin", Enabled: false, DisabledReason: "已在设置里停用"},
	}}
	w := skillRequest(t, handlerWithSkills(t, catalog), http.MethodGet, "/api/skills", false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	first := skillList(t, w.Body.String())
	if len(first) != 2 {
		t.Fatalf("body=%s, want both skills", w.Body.String())
	}
	want := map[string]any{"name": "demo", "description": "Does a demo.", "scope": "user", "enabled": true, "disabled_reason": ""}
	if len(first[0]) != len(want) {
		t.Fatalf("skill=%v, want exactly the keys %v", first[0], want)
	}
	for key, value := range want {
		if first[0][key] != value {
			t.Fatalf("skill[%q]=%v, want %v (skill=%v)", key, first[0][key], value, first[0])
		}
	}
	if len(first[1]) != len(want) {
		t.Fatalf("skill=%v, want the same keys on every entry", first[1])
	}
	if first[1]["enabled"] != false || first[1]["disabled_reason"] != "已在设置里停用" {
		t.Fatalf("skill=%v, want the disabled state and reason", first[1])
	}
}

func TestALongDescriptionIsCutWithTheLossStated(t *testing.T) {
	long := strings.Repeat("字", 300)
	catalog := &fakeCatalog{skills: []SkillRef{{Name: "demo", Description: long, Scope: "user", Enabled: true}}}
	w := skillRequest(t, handlerWithSkills(t, catalog), http.MethodGet, "/api/skills", false)
	description, _ := skillBody(t, w.Body.String())["description"].(string)
	if len([]rune(description)) <= skillDescriptionChars {
		t.Fatalf("description=%q, want it over the limit", description)
	}
	if !strings.Contains(description, "was cut at 200 characters; it is 300 characters long") {
		t.Fatalf("description=%q, want the cut stated", description)
	}
	// The kept part is the start of the real description, and the cut is by
	// rune rather than by byte: cutting mid-rune would hand the browser broken
	// text.
	if !strings.HasPrefix(description, strings.Repeat("字", skillDescriptionChars)) {
		t.Fatalf("description=%q, want the first %d characters", description, skillDescriptionChars)
	}
}

func TestTurningOneSkillOffAndOn(t *testing.T) {
	catalog := &fakeCatalog{skills: []SkillRef{{Name: "demo", Description: "Does a demo.", Scope: "user", Enabled: true}}}
	h := handlerWithSkills(t, catalog)

	w := skillRequest(t, h, http.MethodPost, "/api/skills/demo/disable", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"disabled_reason":"已在设置里停用"`) {
		t.Fatalf("body=%s, want the new state and the reason", w.Body.String())
	}

	w = skillRequest(t, h, http.MethodPost, "/api/skills/demo/enable", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"disabled_reason":""`) {
		t.Fatalf("body=%s, want it back on with no reason", w.Body.String())
	}
	want := []string{"demo=false", "demo=true"}
	if len(catalog.asked) != 2 || catalog.asked[0] != want[0] || catalog.asked[1] != want[1] {
		t.Fatalf("asked=%v, want %v", catalog.asked, want)
	}
}

// The mutation must carry the bound Origin. A path that is not registered in
// requiresOrigin gets no check at all, so this is the test that would fail
// silently — by passing every request — if the endpoint were left out of it.
func TestChangingASkillRequiresTheBoundOrigin(t *testing.T) {
	catalog := &fakeCatalog{skills: []SkillRef{{Name: "demo", Enabled: true}}}
	h := handlerWithSkills(t, catalog)

	if w := skillRequest(t, h, http.MethodPost, "/api/skills/demo/disable", false); w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 without an Origin (body=%s)", w.Code, w.Body.String())
	}
	if len(catalog.asked) != 0 {
		t.Fatalf("asked=%v, want nothing asked of the capability", catalog.asked)
	}
	if w := skillRequest(t, h, http.MethodPost, "/api/skills/demo/disable", true); w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 with the origin (body=%s)", w.Code, w.Body.String())
	}
}

func TestAnUnknownSkillIsNotFound(t *testing.T) {
	catalog := &fakeCatalog{skills: []SkillRef{{Name: "demo", Enabled: true}}}
	w := skillRequest(t, handlerWithSkills(t, catalog), http.MethodPost, "/api/skills/ghost/disable", true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ghost") {
		t.Fatalf("body=%s, want the name it could not find", w.Body.String())
	}
}

func TestASkillStateChangeRejectsOtherMethods(t *testing.T) {
	catalog := &fakeCatalog{skills: []SkillRef{{Name: "demo", Enabled: true}}}
	h := handlerWithSkills(t, catalog)

	if w := skillRequest(t, h, http.MethodGet, "/api/skills/demo/disable", false); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405 (body=%s)", w.Code, w.Body.String())
	}
	if w := skillRequest(t, h, http.MethodPost, "/api/skills", true); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405 on the list endpoint (body=%s)", w.Code, w.Body.String())
	}
	if len(catalog.asked) != 0 {
		t.Fatalf("asked=%v, want nothing asked of the capability", catalog.asked)
	}
}

func TestASkillThatCannotBeChangedIsAServerError(t *testing.T) {
	catalog := &fakeCatalog{skills: []SkillRef{{Name: "demo", Enabled: true}}, err: errors.New("write settings.yaml: permission denied")}
	w := skillRequest(t, handlerWithSkills(t, catalog), http.MethodPost, "/api/skills/demo/disable", true)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body=%s, want the reason", w.Body.String())
	}
}

// A runtime assembled without the Skills capability lists no skills rather than
// failing a read request, and refuses to change anything rather than pretending
// it did.
func TestWithoutACatalogTheListIsEmptyAndChangesAreRefused(t *testing.T) {
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	h := New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"})

	w := skillRequest(t, h, http.MethodGet, "/api/skills", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"skills":[]`) {
		t.Fatalf("status=%d body=%s, want an empty list", w.Code, w.Body.String())
	}
	if w := skillRequest(t, h, http.MethodPost, "/api/skills/demo/disable", true); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
}
