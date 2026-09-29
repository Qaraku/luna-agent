package workspace

import (
	"context"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptionalProjectRulesDoNotBypassReadPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, RulesFileName), []byte("private-rule-content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, bound := range []bool{false, true} {
		for _, decision := range []plugin.AccessDecision{plugin.DecisionAsk, plugin.DecisionDeny} {
			options := Options{Root: root, Rules: "private-rule-content"}
			if bound {
				options.Lookup = func(string) (Target, bool) { return Target{Name: "project", Dirs: []string{root}}, true }
			}
			p, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			policy := plugin.DefaultAccessPolicy()
			policy.Read = decision
			ctx := plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", Permissions: &policy, Approve: func(context.Context, plugin.AccessRequest) error {
				t.Fatal("optional context must not create implicit approval loops")
				return nil
			}})
			blocks, err := p.Contexts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(blocks) != 1 || strings.Contains(blocks[0].Text, "private-rule-content") {
				t.Fatalf("read=%s bound=%v blocks=%+v", decision, bound, blocks)
			}
		}
	}
}
