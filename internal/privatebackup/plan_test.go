package privatebackup

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanSizeAndRoundTrip(t *testing.T) {
	p := Plan{Format: 1, ConfigDir: "/fixture/config", ConfigFile: "/fixture/config/config.yaml", DataDir: "/fixture/data", StateDir: "/fixture/data", SessionsDir: "/fixture/data/sessions"}
	raw, _ := json.Marshal(p)
	if _, err := DecodePlan(raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		p.SkillDirs = append(p.SkillDirs, "/"+strings.Repeat("a", 4000))
	}
	if err := p.Validate(); err == nil {
		t.Fatal("generated a plan that cannot be read back")
	}
}
