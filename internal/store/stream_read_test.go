package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 大部分日志是工具结果，列表和模型历史都不应为这些无关内容保留整份解码副本。
func projectionFixture(t testing.TB) (*Store, string, int64) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Create("large tool history")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	if err := store.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "question", At: at}); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", 64<<10)
	for i := 0; i < 128; i++ {
		if err := store.AppendToolCall(id, ToolCallRecord{Name: "fixture", Result: text, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AppendMessage(id, MessageRecord{Role: RoleAssistant, Text: "answer", At: at}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.path(id))
	if err != nil {
		t.Fatal(err)
	}
	return store, id, info.Size()
}

func TestSessionProjectionsAvoidWholeLogCopies(t *testing.T) {
	store, id, size := projectionFixture(t)
	for _, operation := range []string{"list", "messages"} {
		t.Run(operation, func(t *testing.T) {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if operation == "list" {
				summaries, err := store.List()
				if err != nil || len(summaries) != 1 {
					t.Fatalf("list count=%d error=%v", len(summaries), err)
				}
			} else {
				messages, err := store.Messages(id)
				if err != nil || len(messages) != 2 {
					t.Fatalf("message count=%d error=%v", len(messages), err)
				}
			}
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			// 已包含逐条 JSON 解码所需分配，并额外留出一份日志及 1 MiB 余量。
			// 这是针对合成大工具结果的回归界限，不是限制用户日志的大小。
			budget := uint64(size)*2 + 1<<20
			t.Logf("%s: log=%d bytes allocated=%d bytes budget=%d bytes", operation, size, allocated, budget)
			if allocated > budget {
				t.Errorf("%s still copies the whole tool history: allocated=%d budget=%d", operation, allocated, budget)
			}
		})
	}
}

func BenchmarkSessionProjection(b *testing.B) {
	for _, operation := range []string{"list", "messages", "full_read"} {
		b.Run(operation, func(b *testing.B) {
			store, id, size := projectionFixture(b)
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				switch operation {
				case "list":
					_, err = store.List()
				case "messages":
					_, err = store.Messages(id)
				default:
					_, err = store.Read(id)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSessionProjectionStillValidatesUnselectedRecords(t *testing.T) {
	for _, line := range []string{
		"{\"type\":\"tool_call\",\"result\":42}\n",
		"{\"type\":\"unknown\"}\n",
		"broken JSON\n",
	} {
		store := open(t)
		id, err := store.Create("kept")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AppendMessage(id, MessageRecord{Text: "valid", Role: RoleUser}); err != nil {
			t.Fatal(err)
		}
		path := store.path(id)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data := append(append(original, '\n'), []byte(line)...)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read(id); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "line 4") {
			t.Errorf("Read hid corruption or lost its physical line: %v", err)
		}
		if list, err := store.List(); !errors.Is(err, ErrCorrupt) || list != nil {
			t.Errorf("List returned partial results or hid corruption: count=%d err=%v", len(list), err)
		}
		if messages, err := store.Messages(id); !errors.Is(err, ErrCorrupt) || messages != nil {
			t.Errorf("Messages returned partial results or hid corruption: count=%d err=%v", len(messages), err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, after) {
			t.Fatal("a read changed the corrupt file")
		}
	}
}

func TestSessionReadsKeepLargeLinesAndIgnoreOnlyTheTornTail(t *testing.T) {
	for _, size := range []int{4095, 4096, 4097, 65536, 256 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store := open(t)
			id, err := store.Create("large line")
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Repeat("界", size)
			if err := store.AppendMessage(id, MessageRecord{Role: RoleAssistant, Text: text}); err != nil {
				t.Fatal(err)
			}
			path := store.path(id)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// 一个跨过多个缓冲区、却没有结束换行的尾片段，仍须整体忽略。
			data = append(data, strings.Repeat("torn", 20<<10)...)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			session, err := store.Read(id)
			if err != nil || !session.Truncated || len(session.Records) != 2 || session.Records[1].Message.Text != text {
				t.Fatalf("large record changed: records=%d truncated=%v err=%v", len(session.Records), session.Truncated, err)
			}
			messages, err := store.Messages(id)
			if err != nil || len(messages) != 1 || messages[0].Text != text {
				t.Fatal("large message was dropped or capped")
			}
			if _, err := store.List(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEmptySessionProjectionKeepsItsFallbackAndSliceShape(t *testing.T) {
	for _, content := range []string{"", "\n", "unfinished", " \n "} {
		store := open(t)
		id, err := store.Create("discarded fixture header")
		if err != nil {
			t.Fatal(err)
		}
		path := store.path(id)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		session, err := store.Read(id)
		truncated := content != "" && !strings.HasSuffix(content, "\n")
		if err != nil || session.Title != "" || !session.CreatedAt.Equal(at) || !session.UpdatedAt.Equal(at) || session.Truncated != truncated || (session.Records == nil) != (content == "") || len(session.Records) != 0 {
			t.Errorf("fallback or empty slice shape changed for %q: %+v, %v", content, session, err)
		}
	}
}
