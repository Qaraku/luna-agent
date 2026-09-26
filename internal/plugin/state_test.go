package plugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirForResolvesTheClaimedNamespace(t *testing.T) {
	d := withClaims(desc("memory", DeploymentBuiltin), Claim{Kind: ClaimStateNamespace, ID: ".runtime"})
	got, err := StateDirFor(d, "/repo")
	if err != nil {
		t.Fatalf("StateDirFor: %v", err)
	}
	if want := filepath.Join("/repo", ".runtime"); got != want {
		t.Fatalf("StateDirFor = %q, want %q", got, want)
	}
}

func TestStateDirForErrors(t *testing.T) {
	cases := []struct {
		name    string
		d       Descriptor
		root    string
		wantErr string
	}{
		{"no state namespace claimed", desc("plain", DeploymentBuiltin), "/repo", "claims no state namespace"},
		{"relative state root", withClaims(desc("memory", DeploymentBuiltin), Claim{Kind: ClaimStateNamespace, ID: ".runtime"}), "repo", "must be absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StateDirFor(tc.d, tc.root)
			if err == nil {
				t.Fatalf("StateDirFor = %q, want an error", got)
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
			d:    withContrib(desc(id, DeploymentBuiltin), Contribution{Kind: ContributionContext, ID: "notes"}),
			list: []ContextBlock{{ID: "notes", Kind: ContextReference, Text: id}},
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
