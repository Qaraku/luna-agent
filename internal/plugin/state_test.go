package plugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirResolvesTheClaimedNamespace(t *testing.T) {
	reg := NewRegistry()
	p := barePlugin{d: withClaims(desc("memory", DeploymentBuiltin), Claim{Kind: ClaimStateNamespace, ID: ".runtime"})}
	if err := reg.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := reg.StateDir("memory", "/repo")
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := filepath.Join("/repo", ".runtime"); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

func TestStateDirErrors(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(barePlugin{d: desc("plain", DeploymentBuiltin)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(barePlugin{d: withClaims(desc("memory", DeploymentBuiltin),
		Claim{Kind: ClaimStateNamespace, ID: ".runtime"})}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		id      string
		root    string
		wantErr string
	}{
		{"unknown plugin", "ghost", "/repo", "is not registered"},
		{"plugin without a state namespace", "plain", "/repo", "claims no state namespace"},
		{"relative state root", "memory", "repo", "must be absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reg.StateDir(tc.id, tc.root)
			if err == nil {
				t.Fatalf("StateDir = %q, want an error", got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to name %q", err, tc.wantErr)
			}
		})
	}
}

// 上下文贡献的 ID 只在本插件内有意义，因此两个插件可以各自声明同名的一项；
// 工具名、路由路径与面板 id 则相反，由注册表拒绝重名（见 registry_test.go）。
func TestContextContributionIdsAreNotGlobal(t *testing.T) {
	reg := NewRegistry()
	mk := func(id string) contextPlugin {
		return contextPlugin{
			d:    withContrib(desc(id, DeploymentBuiltin), Contribution{Kind: ContributionContext, ID: "facts"}),
			list: []ContextBlock{{ID: "facts", Kind: ContextReference, Text: id}},
		}
	}
	if err := reg.Register(mk("first")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := reg.Register(mk("second")); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := len(reg.Entries()); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
}
