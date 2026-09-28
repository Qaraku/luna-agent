package filewrite

import (
	"fmt"
	"strings"
)

// diffText renders the change from old to new as one block: every line the old
// text loses, then every line the new text gains. It returns the rendered body
// and the two counts the result header states.
//
// The algorithm is the two cheap ends of a diff and nothing more: drop the
// lines the two texts share at the front, drop the lines they share at the back,
// and render what is left as a single change block. It is deliberately not a
// minimal edit script — no LCS, no Myers, and therefore no new dependency.
//
// The trade-off is stated rather than hidden. When one place in the middle of a
// file changes, the block is small (the changed line, and its neighbours only if
// they differ too) and reads exactly like a unified diff. When several unrelated
// changes are spread through a file, the block covers everything between the
// first and the last of them — including the untouched lines in between, which
// are rendered once as removed and once as added. A minimal edit script would
// print those lines not at all. What the trade-off buys is worth more here: the
// answer is exact about the two ends that matter (what the file began with and
// ended with are unchanged, and the diff does not print them), it costs one pass
// over the text, and it cannot go quadratic on a model-supplied file.
//
// A call that adds lines to the end of a file, or that changes it entirely, is
// the ordinary case for this tool, and for those the block is already minimal;
// the block is also bounded by fitDiff, so a large one is cut with a stated
// limit rather than returned in full.
func diffText(old, new string) (body string, added, removed int) {
	oldLines := splitLines(old)
	newLines := splitLines(new)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	removedLines := oldLines[prefix : len(oldLines)-suffix]
	addedLines := newLines[prefix : len(newLines)-suffix]

	var b strings.Builder
	for _, line := range removedLines {
		writeDiffLine(&b, "-", line)
	}
	for _, line := range addedLines {
		writeDiffLine(&b, "+", line)
	}
	return b.String(), len(addedLines), len(removedLines)
}

// writeDiffLine renders one diff line with its marker. A line keeps its own line
// ending, so a file whose last line has none is shown as such instead of the
// tool inventing one; the marker goes in front of the line's text.
func writeDiffLine(b *strings.Builder, marker, line string) {
	b.WriteString(marker)
	b.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		b.WriteString("\n")
	}
}

// splitLines cuts text into lines that keep their own line ending. An empty text
// has no lines rather than one empty line, and the line ending at the end of a
// text is not a line of its own.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if last := lines[len(lines)-1]; last == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// fitDiff renders the diff inside MaxDiffBytes.
//
// A diff that fits is returned whole. A diff that does not is cut on a line
// boundary and followed by a line saying which limit it hit and how many bytes
// of the diff are not shown — a result that stopped in the middle of a change
// without saying so would be read as the whole of it. The counts in the header
// above are the counts of the full change, not of the part that fits: they say
// what the call did to the file, and the note says how much of the evidence is
// missing.
//
// The one case with nothing to show is a single diff line longer than the limit
// on its own; there is no honest partial line here, so the note says that
// instead of printing a cut line as if it were the line.
func fitDiff(body string) string {
	if len(body) <= MaxDiffBytes {
		return body
	}
	cut := body[:MaxDiffBytes]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i+1]
	} else {
		cut = ""
	}
	note := fmt.Sprintf("… the diff was cut at the %d-byte limit; %d more bytes of it are not shown\n", MaxDiffBytes, len(body)-len(cut))
	if cut == "" {
		note = fmt.Sprintf("… the diff is not shown: its first line alone is longer than the %d-byte limit\n", MaxDiffBytes)
	}
	return cut + note
}
