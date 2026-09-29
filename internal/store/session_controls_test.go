package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionControlFieldsRoundTripWithoutRewriting(t *testing.T) {
	for _, effort := range []any{nil, "", "high", "none"} {
		t.Run("effort", func(t *testing.T) {
			disk, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id := "0123456789abcdef"
			header := map[string]any{"type": "session", "id": id, "title": "kept", "created_at": "2026-01-01T00:00:00Z"}
			config := map[string]any{"type": "config", "model": "m", "workspace": "w"}
			if effort != nil {
				config["reasoning_effort"] = effort
			}
			var bytes []byte
			for _, record := range []any{header, config} {
				line, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				bytes = append(bytes, append(line, '\n')...)
			}
			path := filepath.Join(disk.Dir(), id+FileSuffix)
			if err := os.WriteFile(path, bytes, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := disk.Read(id)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(got.Config)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip map[string]any
			if err := json.Unmarshal(raw, &roundTrip); err != nil {
				t.Fatal(err)
			}
			value, present := roundTrip["reasoning_effort"]
			if (effort != nil) != present || (present && value != effort) {
				t.Fatalf("effort=%v, stored=%s", effort, raw)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(bytes) {
				t.Fatal("reading rewrote history")
			}
		})
	}
}

func TestOnlyOptedInEmptySessionTitlesFollowFirstUserMessage(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		auto        bool
		want        string
	}{
		{"new empty", "", true, "第一条 用户消息"},
		{"legacy empty", "", false, ""},
		{"explicit title", "我的项目", true, "我的项目"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disk, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id := "0123456789abcdef"
			rows := []any{map[string]any{"type": "session", "id": id, "title": tc.title, "auto_title": tc.auto}, map[string]any{"type": "message", "role": "user", "text": "第一条\n用户消息"}, map[string]any{"type": "message", "role": "user", "text": "第二条消息"}}
			var bytes []byte
			for _, row := range rows {
				raw, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				bytes = append(bytes, append(raw, '\n')...)
			}
			if err := os.WriteFile(filepath.Join(disk.Dir(), id+FileSuffix), bytes, 0600); err != nil {
				t.Fatal(err)
			}
			session, err := disk.Read(id)
			if err != nil {
				t.Fatal(err)
			}
			list, err := disk.List()
			if err != nil {
				t.Fatal(err)
			}
			if session.Title != tc.want || len(list) != 1 || list[0].Title != tc.want {
				t.Fatalf("title=%q summaries=%+v want=%q", session.Title, list, tc.want)
			}
		})
	}
}
