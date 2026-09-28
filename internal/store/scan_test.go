package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fragmentedSessionReader struct {
	input io.Reader
	chunk int
}

func (r fragmentedSessionReader) Read(p []byte) (int, error) {
	if len(p) > r.chunk {
		p = p[:r.chunk]
	}
	return r.input.Read(p)
}

// 对照旧的整文件切行语义，验证任意分片、空白与尾片段不改变成功结果或错误位置。
func FuzzSessionRecordScan(f *testing.F) {
	for _, data := range [][]byte{
		nil, []byte("\n "), []byte("{}\n"),
		[]byte("{\"type\":\"message\",\"text\":\"ok\"}\n"),
		[]byte("\n{\"type\":\"message\",\"text\":\"ok\"}\r\ntrailing"),
		[]byte("{\"type\":\"tool_call\",\"result\":\"" + strings.Repeat("x", 65537) + "\"}\n"),
	} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		parts := strings.Split(string(data), "\n")
		wantTruncated := parts[len(parts)-1] != ""
		var expected []Record
		var expectedErr error
		for i, line := range parts[:len(parts)-1] {
			if strings.TrimSpace(line) == "" {
				continue
			}
			record, err := decodeLine([]byte(line))
			if err != nil {
				expectedErr = fmt.Errorf("%w snapshot.jsonl line %d: %v", ErrCorrupt, i+1, err)
				break
			}
			expected = append(expected, record)
		}
		chunk := 1
		if len(data) > 0 {
			chunk += int(data[0]) % 63
		}
		var actual []Record
		truncated, err := scanRecords(fragmentedSessionReader{input: bytes.NewReader(data), chunk: chunk}, "/fixture/snapshot.jsonl", func(record Record) { actual = append(actual, record) })
		if (err != nil) != (expectedErr != nil) {
			t.Fatalf("error mismatch: got %v want %v", err, expectedErr)
		}
		if err != nil {
			if err.Error() != expectedErr.Error() {
				t.Fatalf("error location changed: got %v want %v", err, expectedErr)
			}
			return
		}
		if truncated != wantTruncated || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("scan differs from complete-line parsing: truncated=%v want=%v records=%d want=%d", truncated, wantTruncated, len(actual), len(expected))
		}
	})
}

type failedSessionReader struct{ err error }

func (r failedSessionReader) Read([]byte) (int, error) { return 0, r.err }

func TestSessionScanDoesNotTreatReadFailureAsATornTail(t *testing.T) {
	sentinel := errors.New("synthetic read failure")
	source := io.MultiReader(strings.NewReader("{\"type\":\"message\",\"text\":\"kept\"}\npartial"), failedSessionReader{err: sentinel})
	seen := 0
	_, err := scanRecords(source, "fixture.jsonl", func(Record) { seen++ })
	if !errors.Is(err, sentinel) || seen != 1 {
		t.Fatalf("read failure was swallowed: records=%d err=%v", seen, err)
	}
}

func TestSessionReadDoesNotChaseNewlyAppendedRecords(t *testing.T) {
	store := open(t)
	id, err := store.Create("bounded read")
	if err != nil {
		t.Fatal(err)
	}
	wrote := false
	session, err := store.readFile(store.path(id), id, true, func(Record) {
		if wrote {
			return
		}
		wrote = true
		if err := store.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "appended after reading began", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil || len(session.Records) != 1 {
		t.Fatalf("read followed a later append: records=%d err=%v", len(session.Records), err)
	}
	later, err := store.Read(id)
	if err != nil || len(later.Records) != 2 {
		t.Fatalf("later read lost the appended record: records=%d err=%v", len(later.Records), err)
	}
}

func TestSessionReadReuseDoesNotChangeEarlierMessages(t *testing.T) {
	store := open(t)
	id, err := store.Create("buffer reuse")
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{strings.Repeat("first", 5000), strings.Repeat("第二", 5000), strings.Repeat("third", 5000)}
	for _, text := range texts {
		if err := store.AppendMessage(id, MessageRecord{Role: RoleUser, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := store.Messages(id)
	if err != nil || len(messages) != len(texts) {
		t.Fatalf("messages=%d err=%v", len(messages), err)
	}
	for i, text := range texts {
		if messages[i].Text != text {
			t.Errorf("buffer reuse changed message %d", i)
		}
	}
}
