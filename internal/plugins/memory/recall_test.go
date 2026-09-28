package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

func recallFixture(t *testing.T, texts ...string) *Store {
	t.Helper()
	store := newRememberStore(t)
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	for i, text := range texts {
		if _, err := store.Remember("source-session-only", text, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func recallJSON(t *testing.T, input any) string {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func recalledTexts(result string) []string {
	var texts []string
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "- ") {
			text, _, _ := strings.Cut(strings.TrimPrefix(line, "- "), " (recorded ")
			texts = append(texts, text)
		}
	}
	return texts
}

func TestRecallQueryFindsFactsOutsideTheDefaultWindow(t *testing.T) {
	texts := []string{"最早的偏好：喜欢乌龙茶"}
	for i := 1; i < 60; i++ {
		texts = append(texts, fmt.Sprintf("recent fact %02d", i))
	}
	store := recallFixture(t, texts...)
	before := storedBytes(t, store)
	if got := recallOnce(t, store, `{}`); strings.Contains(got, texts[0]) {
		t.Fatal("fixture did not put the target outside the default window")
	}
	older := recalledTexts(recallOnce(t, store, `{"offset":50}`))
	if len(older) != 10 || older[0] != texts[0] || older[9] != texts[9] {
		t.Fatalf("continuing the default window did not return the older facts: %v", older)
	}
	got := recallOnce(t, store, `{"query":"乌龙茶"}`)
	if actual := recalledTexts(got); len(actual) != 1 || actual[0] != texts[0] {
		t.Fatalf("query missed the old effective fact: %s", got)
	}
	for _, want := range []string{"stored facts in effect: 60", "matching facts: 1", "returned: 1", "next_offset: none"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if !bytes.Equal(before, storedBytes(t, store)) {
		t.Fatal("search changed the stored bytes")
	}
}

func TestRecallPagesCoverEveryMatchOnce(t *testing.T) {
	var texts, matches []string
	for i := 0; i < 67; i++ {
		text := fmt.Sprintf("match %02d", i)
		texts = append(texts, "unrelated", text)
		matches = append(matches, text)
	}
	store := recallFixture(t, texts...)
	before := storedBytes(t, store)
	seen := map[string]bool{}
	for offset := 0; ; {
		got := recallOnce(t, store, recallJSON(t, map[string]any{"query": "MATCH", "offset": offset, "limit": 13}))
		page := recalledTexts(got)
		end := len(matches) - offset
		start := max(0, end-13)
		want := matches[start:end]
		if strings.Join(page, "|") != strings.Join(want, "|") {
			t.Fatalf("offset %d: got %v, want %v; %s", offset, page, want, got)
		}
		for _, text := range page {
			if seen[text] {
				t.Fatalf("repeated fact %q", text)
			}
			seen[text] = true
		}
		for _, metadata := range []string{"stored facts in effect: 134", "matching facts: 67", fmt.Sprintf("returned: %d", len(page)), fmt.Sprintf("remaining older matches: %d", start)} {
			if !strings.Contains(got, metadata) {
				t.Fatalf("missing %q: %s", metadata, got)
			}
		}
		if start == 0 {
			if !strings.Contains(got, "next_offset: none") {
				t.Fatalf("last page offers another page: %s", got)
			}
			break
		}
		var next int
		found := false
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "next_offset: ") {
				var err error
				next, err = strconv.Atoi(strings.TrimPrefix(line, "next_offset: "))
				if err != nil {
					t.Fatal(err)
				}
				found = true
			}
		}
		if !found || next != offset+len(page) {
			t.Fatalf("invalid continuation from offset %d: %s", offset, got)
		}
		offset = next
	}
	if len(seen) != len(matches) || !bytes.Equal(before, storedBytes(t, store)) {
		t.Fatal("pagination lost facts or changed the store")
	}
}

func TestRecallQueryIsLiteralCaseInsensitiveAndTextOnly(t *testing.T) {
	texts := []string{"writes Go", "偏好中文回答", "pattern .* is literal", "pattern anything is literal", "Greek Σ", "line one\nline two"}
	store := recallFixture(t, texts...)
	for _, tc := range []struct{ query, want string }{
		{"GO", texts[0]}, {"中文", texts[1]}, {".*", texts[2]}, {"ς", texts[4]},
		{"one\nline", texts[5]}, {"source-session-only", ""}, {"2026-09-28", ""}, {"absent", ""}, {"[", ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got := recallOnce(t, store, recallJSON(t, map[string]any{"query": tc.query}))
			actual := recalledTexts(got)
			if tc.want == "" {
				if len(actual) != 0 || !strings.Contains(got, "no facts match this query") {
					t.Fatalf("non-text match or dishonest empty result: %s", got)
				}
			} else if len(actual) != 1 || actual[0] != singleLine(tc.want) {
				t.Fatalf("literal query %q: %s", tc.query, got)
			}
			if strings.Contains(got, "\nline two") || strings.Contains(got, "\nline\"") {
				t.Fatalf("a query or fact opened a raw line: %q", got)
			}
		})
	}
}

func TestRecallDistinguishesEmptyStoreNoMatchAndEmptyPage(t *testing.T) {
	store := recallFixture(t, "one", "two")
	for _, offset := range []int{2, 3, int(^uint(0) >> 1)} {
		got := recallOnce(t, store, recallJSON(t, map[string]any{"offset": offset}))
		if len(recalledTexts(got)) != 0 || !strings.Contains(got, "no facts on this page") || !strings.Contains(got, "matching facts: 2") || !strings.Contains(got, "next_offset: none") {
			t.Fatalf("offset %d: %s", offset, got)
		}
	}
	got := recallOnce(t, recallFixture(t), `{"query":"one"}`)
	if !strings.Contains(got, recallEmpty) || !strings.Contains(got, "stored facts in effect: 0") {
		t.Fatalf("empty store: %s", got)
	}
}

func TestRecallOptionalDefaultsPreserveTheExistingListing(t *testing.T) {
	for _, count := range []int{0, 2, 60} {
		var texts []string
		for i := 0; i < count; i++ {
			texts = append(texts, fmt.Sprintf("fact %d", i))
		}
		store := recallFixture(t, texts...)
		want := recallOnce(t, store, `{}`)
		for _, input := range []string{"", " \n ", "null", `{"query":""}`, `{"offset":0}`, `{"limit":50}`, `{"query":"","offset":0,"limit":50}`} {
			if got := recallOnce(t, store, input); got != want {
				t.Errorf("%d facts, %s changed the default listing:\n%s\nwant:\n%s", count, input, got, want)
			}
		}
	}
}

func TestRecallValidatesNewParametersBeforeReadingTheStore(t *testing.T) {
	store := recallFixture(t, "kept")
	if err := os.WriteFile(store.path, []byte("corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := storedBytes(t, store)
	for _, input := range []string{
		`{"limit":0}`, `{"limit":51}`, `{"limit":-1}`, `{"offset":-1}`,
		`{"offset":1.5}`, `{"offset":9223372036854775808}`, `{"limit":"5"}`, `{"query":1}`,
		`{"query":null}`, `{"offset":null}`, `{"limit":null}`, `{"extra":1}`,
		`{"query":"ok"} {}`, `[]`, `"query"`,
	} {
		_, err := NewRecallTool(store).Invoke(runCtx("s"), input)
		if err == nil || plugin.IsUnavailable(err) || errors.Is(err, ErrCorrupt) {
			t.Errorf("%s should refuse before any read: %v", input, err)
		}
	}
	if !bytes.Equal(before, storedBytes(t, store)) {
		t.Fatal("refused query changed the stored bytes")
	}
	if _, err := NewRecallTool(store).Invoke(runCtx("s"), `{"query":"kept"}`); !plugin.IsUnavailable(err) || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("valid query hid the corrupt store: %v", err)
	}
}

func TestRecallReadsCurrentEffectiveFactsOnEveryPage(t *testing.T) {
	store := recallFixture(t, "match old", "match newer")
	facts, err := store.Facts()
	if err != nil {
		t.Fatal(err)
	}
	if got := recalledTexts(recallOnce(t, store, `{"query":"match","limit":1}`)); len(got) != 1 || got[0] != "match newer" {
		t.Fatalf("first page: %v", got)
	}
	if _, err := store.Retract(facts[1].At, facts[1].Text); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember("s", "match newest", time.Now()); err != nil {
		t.Fatal(err)
	}
	before := storedBytes(t, store)
	got := recallOnce(t, store, `{"query":"match"}`)
	if texts := recalledTexts(got); strings.Join(texts, "|") != "match old|match newest" {
		t.Fatalf("stale or retracted facts: %s", got)
	}
	if !strings.Contains(got, "changes may shift offsets") || !bytes.Equal(before, storedBytes(t, store)) {
		t.Fatalf("missing live-window warning or changed store: %s", got)
	}
}

// 没命中的长查询不应被整段回显，从而绕过事实窗口对返回体的约束。
func TestRecallLongQueryDoesNotExpandAnEmptyResult(t *testing.T) {
	query := strings.Repeat("并不存在", MaxFactChars)
	got := recallOnce(t, recallFixture(t, "one short fact"), recallJSON(t, map[string]any{"query": query}))
	if !strings.Contains(got, "no facts match this query") || len(got) > 1024 || strings.Contains(got, query) {
		t.Fatalf("empty result expanded with query length: %d bytes", len(got))
	}
}
