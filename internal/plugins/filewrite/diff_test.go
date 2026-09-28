package filewrite

import (
	"fmt"
	"strings"
	"testing"
)

// The diff is the one block between the ends the two texts share: what the file
// began with and ended with is not printed, and what is left is rendered once as
// removed and once as added.
//
// The last two cases pin the trade-off the implementation states in its comment:
// changes spread through the middle of a file put the untouched lines between
// them into the block, once on each side. A minimal edit script would not print
// them; this is not one, and the test says so out loud rather than pretending
// the block is the smallest possible.
func TestDiffTextRendersTheChangeAsOneBlock(t *testing.T) {
	cases := []struct {
		name    string
		old     string
		new     string
		body    string
		added   int
		removed int
	}{
		{
			name:  "a file created from nothing",
			old:   "",
			new:   "a\nb\n",
			body:  "+a\n+b\n",
			added: 2,
		},
		{
			name: "the same content",
			old:  "a\n", new: "a\n",
		},
		{
			name:    "one line changed in the middle",
			old:     "a\nb\nc\n",
			new:     "a\nB\nc\n",
			body:    "-b\n+B\n",
			added:   1,
			removed: 1,
		},
		{
			name:  "a line appended",
			old:   "a\n",
			new:   "a\nb\n",
			body:  "+b\n",
			added: 1,
		},
		{
			name:    "a line removed",
			old:     "a\nb\n",
			new:     "a\n",
			body:    "-b\n",
			removed: 1,
		},
		{
			name:    "a file whose last line has no newline",
			old:     "a\nb",
			new:     "a\nc",
			body:    "-b\n+c\n",
			added:   1,
			removed: 1,
		},
		{
			name:  "a file that is only a newline",
			old:   "",
			new:   "\n",
			body:  "+\n",
			added: 1,
		},
		{
			name:    "nothing but a change in the middle",
			old:     "old\n",
			new:     "new\n",
			body:    "-old\n+new\n",
			added:   1,
			removed: 1,
		},
		{
			name:    "two changes with an unchanged line between them",
			old:     "x\na\ny\nb\nz\n",
			new:     "x\nA\ny\nB\nz\n",
			body:    "-a\n-y\n-b\n+A\n+y\n+B\n",
			added:   3,
			removed: 3,
		},
		{
			name:    "a line inserted at the end repeated earlier in the file",
			old:     "a\nb\n",
			new:     "a\nb\na\n",
			body:    "+a\n",
			added:   1,
			removed: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, added, removed := diffText(tc.old, tc.new)
			if body != tc.body || added != tc.added || removed != tc.removed {
				t.Fatalf("diffText(%q, %q) = %q, %d, %d; want %q, %d, %d",
					tc.old, tc.new, body, added, removed, tc.body, tc.added, tc.removed)
			}
		})
	}
}

// A diff that fits comes back whole, with nothing added to it.
func TestFitDiffPassesAShortDiffThrough(t *testing.T) {
	body, _, _ := diffText("", strings.Repeat("a line\n", 10))
	if got := fitDiff(body); got != body {
		t.Fatalf("fitDiff returned %q, want %q", got, body)
	}
}

// A diff that does not fit is cut on a line boundary, and the note states the
// limit it hit and counts every byte it could not give.
func TestFitDiffCutsOnALineBoundaryAndCountsWhatItDropped(t *testing.T) {
	full, _, _ := diffText("", strings.Repeat("a line of text\n", 2000))
	if len(full) <= MaxDiffBytes {
		t.Fatalf("the fixture is %d bytes, barely over the %d-byte limit is what this test needs", len(full), MaxDiffBytes)
	}
	got := fitDiff(full)

	marker := "\n… "
	shown, note, ok := strings.Cut(got, marker)
	if !ok {
		t.Fatalf("fitDiff returned %q, want it to say what it did", got)
	}
	shown += "\n"
	if len(shown) > MaxDiffBytes {
		t.Fatalf("the cut diff is %d bytes, over the %d-byte limit", len(shown), MaxDiffBytes)
	}
	if !strings.HasSuffix(shown, "a line of text\n") {
		t.Fatalf("the diff was cut inside a line: %q", shown)
	}
	if !strings.HasPrefix(full, shown) {
		t.Fatalf("the cut diff is not a prefix of the whole one: %q", shown)
	}
	for _, want := range []string{
		fmt.Sprintf("the diff was cut at the %d-byte limit", MaxDiffBytes),
		fmt.Sprintf("%d more bytes of it are not shown", len(full)-len(shown)),
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("note=%q, want it to say %q", note, want)
		}
	}
}

// A single diff line longer than the limit has no honest partial rendering: the
// result says the line is too long instead of printing a part of it as if it
// were the line.
func TestFitDiffSaysWhenOneLineIsLongerThanTheLimit(t *testing.T) {
	full, _, _ := diffText("", "+"+strings.Repeat("x", MaxDiffBytes+1)+"\n")
	if len(full) <= MaxDiffBytes {
		t.Fatalf("the fixture is %d bytes, it must be larger than the %d-byte limit", len(full), MaxDiffBytes)
	}
	got := fitDiff(full)
	if strings.Contains(got, "xxx") {
		t.Fatalf("a cut line was printed as if it were the line: %q", got)
	}
	want := fmt.Sprintf("its first line alone is longer than the %d-byte limit", MaxDiffBytes)
	if !strings.Contains(got, want) {
		t.Fatalf("result=%q, want it to say %q", got, want)
	}
}

// The diff never invents a line ending: a file whose last line has none is
// rendered with the marker in front of the line and nothing after it.
func TestDiffTextKeepsAMissingFinalNewlineVisible(t *testing.T) {
	body, added, removed := diffText("a\n", "a\nb")
	if body != "+b\n" || added != 1 || removed != 0 {
		t.Fatalf("diffText = %q, %d, %d; want \"+b\\n\", 1, 0", body, added, removed)
	}
	// The line itself is what the file ends with; the newline the renderer adds
	// after it belongs to the result, which is a sequence of lines by itself.
	if !strings.HasPrefix(body, "+") || strings.HasPrefix(body, "+\n") {
		t.Fatalf("body=%q keeps no trace of the missing line ending", body)
	}
}
