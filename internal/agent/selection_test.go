package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/runconfig"
)

func TestRunSelectionFiltersToolsAndContextWithoutChangingRegistry(t *testing.T) {
	first := &fakeCapability{id: "first", tool: &fakeCapabilityTool{name: "first_tool"}, blocks: []plugin.ContextBlock{{ID: "guide", Kind: plugin.ContextReference, Text: "first context"}}}
	second := &fakeCapability{id: "second", tool: &fakeCapabilityTool{name: "second_tool"}, blocks: []plugin.ContextBlock{{ID: "guide", Kind: plugin.ContextReference, Text: "second context"}}}
	reg := enabledRegistry(t, first.plugin(), second.plugin())
	m := &toolListModel{captureModel: captureModel{answer: "ok"}}
	transcript := &fakeTranscript{}
	r, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithCapabilities(reg), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	setup := &runconfig.Selection{Owner: "presets", ID: "focus", Instructions: "只输出用户要求的内容。", Capabilities: []string{"first"}}
	if _, err = r.Run(context.Background(), RunRequest{Sink: &collectingSink{}, RunID: "one", SessionID: "session-one", Message: "hi", Setup: setup}); err != nil {
		t.Fatal(err)
	}
	names := offeredNames(m)
	if !slices.Contains(names, "first_tool") || slices.Contains(names, "second_tool") {
		t.Fatalf("tools=%v", names)
	}
	system := m.all()[0][0].Content
	if !strings.Contains(system, "first context") || strings.Contains(system, "second context") || !strings.Contains(system, setup.Instructions) {
		t.Fatalf("unexpected system: %s", system)
	}
	if len(reg.Enabled()) != 2 {
		t.Fatal("per-run selection changed global lifecycle")
	}
	_, _, runs := transcript.snapshot()
	if len(runs) != 1 || runs[0].Configuration == nil || !slices.Equal(runs[0].Configuration.Capabilities, []string{"first"}) {
		t.Fatalf("missing effective receipt: %+v", runs)
	}
	if _, err = r.Run(context.Background(), RunRequest{Sink: &collectingSink{}, RunID: "two", Message: "hi"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(offeredNames(m), "second_tool") {
		t.Fatal("selection leaked into another session")
	}
	if strings.Contains(m.all()[1][0].Content, setup.Instructions) {
		t.Fatal("instructions leaked into another session")
	}
}

func TestExplicitEmptySelectionRemovesAllToolsAndContributedContext(t *testing.T) {
	c := &fakeCapability{id: "notes", tool: &fakeCapabilityTool{name: "notes_tool"}, blocks: []plugin.ContextBlock{{ID: "guide", Kind: plugin.ContextReference, Text: "do not inject"}}}
	m := &toolListModel{captureModel: captureModel{answer: "ok"}}
	r, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithCapabilities(enabledRegistry(t, c.plugin())))
	if err != nil {
		t.Fatal(err)
	}
	setup := &runconfig.Selection{ID: "chat", Capabilities: []string{}, Tools: []string{}}
	if _, err = r.Run(context.Background(), RunRequest{Sink: &collectingSink{}, Message: "hi", Setup: setup}); err != nil {
		t.Fatal(err)
	}
	if len(offeredNames(m)) != 0 {
		t.Fatalf("offered=%v", offeredNames(m))
	}
	if strings.Contains(m.first()[0].Content, "do not inject") {
		t.Fatal("disabled context injected")
	}
}

func TestInvalidSelectionNeverCallsModel(t *testing.T) {
	m := &captureModel{answer: "ok"}
	r, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), RunRequest{Sink: &collectingSink{}, Message: "hi", Setup: &runconfig.Selection{ID: "../invalid"}})
	if err == nil || len(m.all()) != 0 {
		t.Fatalf("invalid selection reached model: %v", err)
	}
}

func TestMessageAndRunReceiptKeepTheAdmittedWorkspace(t *testing.T) {
	transcript := &fakeTranscript{}
	m := &captureModel{answer: "ok"}
	runner, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(context.Background(), RunRequest{SessionID: "aaaaaaaa", RunID: "run", WorkspaceID: "project-a", Message: "scope", Sink: &collectingSink{}}); err != nil {
		t.Fatal(err)
	}
	messages, _, runs := transcript.snapshot()
	for _, message := range messages {
		if message.WorkspaceID == nil || *message.WorkspaceID != "project-a" {
			t.Fatal("message scope was not recorded")
		}
	}
	if len(runs) != 1 || runs[0].Configuration.WorkspaceID != "project-a" {
		t.Fatal("run scope was not recorded")
	}
}
