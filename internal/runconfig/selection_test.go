package runconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectionDistinguishesInheritedAndEmptyResources(t *testing.T) {
	var inherited *Selection
	if !inherited.AllowsCapability("notes") || !inherited.AllowsTool("read") || !inherited.AllowsResource("catalog", "item") {
		t.Fatal("nil selection must inherit available resources")
	}
	s := &Selection{ID: "quiet", Capabilities: []string{}, Tools: []string{}, Resources: map[string][]string{"catalog": {}}}
	if s.AllowsCapability("notes") || s.AllowsTool("read") || s.AllowsResource("catalog", "item") {
		t.Fatal("empty selection must select nothing")
	}
	if !s.AllowsResource("other", "item") {
		t.Fatal("absent resource namespace must inherit")
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var round Selection
	if err = json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	if round.AllowsCapability("notes") || round.AllowsResource("catalog", "item") {
		t.Fatal("serialization widened an empty selection")
	}
}

func TestSelectionCloneHasNoSharedMutableData(t *testing.T) {
	level := "xhigh"
	s := &Selection{ID: "work", Capabilities: []string{"notes"}, Tools: []string{"read"}, Resources: map[string][]string{"catalog": {"one"}}, ReasoningEffort: &level}
	c := s.Clone()
	c.Capabilities[0] = "other"
	c.Tools[0] = "write"
	c.Resources["catalog"][0] = "two"
	*c.ReasoningEffort = "max"
	if s.Capabilities[0] != "notes" || s.Tools[0] != "read" || s.Resources["catalog"][0] != "one" || *s.ReasoningEffort != "xhigh" {
		t.Fatal("snapshot aliases caller state")
	}
}

func TestSelectionValidationBoundsContentAndRejectsDuplicates(t *testing.T) {
	valid := &Selection{Owner: "presets", ID: "work", Title: "工作", Instructions: "按用户要求处理。", ReasoningEffort: ptr("max")}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*Selection{
		{ID: "../escape"}, {ID: "work", Instructions: strings.Repeat("x", MaxInstructionsBytes+1)},
		{ID: "work", Instructions: "x\x00y"}, {ID: "work", Capabilities: []string{"notes", "notes"}},
		{ID: "work", ReasoningEffort: ptr("ultra")}, {ID: "work", Resources: map[string][]string{"../catalog": {"one"}}},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted invalid selection: %+v", bad)
		}
	}
}
func ptr(s string) *string { return &s }
