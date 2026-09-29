package agent

import (
	"context"
	"strings"
	"testing"
)

func TestPersonalInstructionSeparatesBehaviorFromToolManuals(t *testing.T) {
	for _, obsolete := range []string{"local demo", "active candidate", "luna_text_transform", "luna_read_file", "luna_list_dir", "luna_search_files", "luna_find_files"} {
		if strings.Contains(instruction, obsolete) {
			t.Errorf("base instruction still contains tool-specific or demo text %q", obsolete)
		}
	}
	for _, required := range []string{"私人", "授权", "预设", "不可信", "the tool refused this call:", "未验证"} {
		if !strings.Contains(instruction, required) {
			t.Errorf("missing behavioral boundary %q", required)
		}
	}
	if len(instruction) > 8192 {
		t.Fatal("base instruction exceeded its budget")
	}
}

func TestFileReadToolOwnsPartialReadGuidance(t *testing.T) {
	info, err := NewReadFileTool(&recordingReader{}).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"start_line", "max_lines", "binary", "limit", "remaining"} {
		if !strings.Contains(info.Desc, required) {
			t.Errorf("file tool description lacks %q", required)
		}
	}
}

func TestCoreDevelopmentInstructionKeepsCandidateAndRuntimeSeparate(t *testing.T) {
	for _, required := range []string{"luna dev", "source/", "LUNA_HOME", "候选补丁", "不在后台覆盖"} {
		if !strings.Contains(instruction, required) {
			t.Errorf("missing controlled self-development guidance %q", required)
		}
	}
}
