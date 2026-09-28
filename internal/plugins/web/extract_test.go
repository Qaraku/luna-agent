package web

import (
	"net/url"
	"strings"
	"testing"
)

// pageURL is the address a test's HTML was fetched from; relative links resolve against it.
func pageURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

// The reader is a heuristic, so what it produces is pinned by whole outputs rather than by
// a general claim: every case here is exactly what one document becomes, and a test that
// changes the reader's mind about any of them has to say so in the diff.
func TestExtractTurnsHtmlIntoReadableText(t *testing.T) {
	const base = "http://example.com/a/b/page.html"
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "the title becomes the first line as a level-1 heading",
			source: `<html><head><title>A &amp; B</title></head><body><p>x</p></body></html>`,
			want:   "# A & B\n\nx\n",
		},
		{
			name:   "only a title is still a title",
			source: `<head><title>T</title></head>`,
			want:   "# T\n",
		},
		{
			name:   "headings keep their level",
			source: `<body><h1>one</h1><h2>two</h2><h3>three</h3><h4>four</h4><h5>five</h5><h6>six</h6></body>`,
			want:   "# one\n\n## two\n\n### three\n\n#### four\n\n##### five\n\n###### six\n",
		},
		{
			name:   "list items become dashes, one per line",
			source: `<p>a list</p><ul><li>first</li><li>second</li><li>third</li></ul>`,
			want:   "a list\n\n- first\n- second\n- third\n",
		},
		{
			name:   "a link keeps its text and gains its absolute address",
			source: `<p>See <a href="/docs/x.html">the docs</a> now.</p>`,
			want:   "See the docs <http://example.com/docs/x.html> now.\n",
		},
		{
			name:   "a relative link is resolved against the address it came from",
			source: `<p><a href="other.html">other</a></p>`,
			want:   "other <http://example.com/a/b/other.html>\n",
		},
		{
			name:   "a link that is not an address keeps only its text",
			source: `<p><a href="javascript:void(0)">click</a> and <a href="#part">jump</a></p>`,
			want:   "click and jump\n",
		},
		{
			name:   "a link whose address is only reachable through a base still gets one",
			source: `<p>a <a href="http://other.example/x">plain</a> link</p>`,
			want:   "a plain <http://other.example/x> link\n",
		},
		{
			name:   "preformatted text keeps its whitespace",
			source: "<p>x</p><pre>a  b\n   c\n</pre>",
			want:   "x\n\na  b\n   c\n",
		},
		{
			name:   "inline code keeps its whitespace and stays in the sentence",
			source: `<p>run <code>go  test</code> now</p>`,
			want:   "run go  test now\n",
		},
		{
			name:   "entities are decoded by the html package",
			source: `<p>a &amp; b &#39;c&#39; &lt;tag&gt;</p>`,
			want:   "a & b 'c' <tag>\n",
		},
		{
			name:   "blocks are separated by a blank line and outside whitespace collapses",
			source: "<div>\n  <p>one\n\n   two</p>\n  <p>three</p>\n</div>",
			want:   "one two\n\nthree\n",
		},
		{
			name:   "inline markup does not split a sentence",
			source: `<p>a <b>bold</b> and <i>slanted</i> line.</p>`,
			want:   "a bold and slanted line.\n",
		},
		{
			name:   "a plain text document is its own text",
			source: "just text\n",
			want:   "just text\n",
		},
		{
			name:   "an empty document has no text",
			source: "",
			want:   "",
		},
		{
			name:   "a document with nothing readable in it has no text",
			source: `<html><head><title>x</title></head><body></body></html>`,
			want:   "# x\n",
		},
		{
			name:   "breaks and rules separate blocks without leaving empty ones",
			source: `<p>one<br>two</p><hr><p>three</p>`,
			want:   "one\ntwo\n\nthree\n",
		},
		{
			name:   "table cells are read as blocks",
			source: `<table><tr><td>left</td><td>right</td></tr></table>`,
			want:   "left\n\nright\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Extract(tc.source, pageURL(t, base)); got != tc.want {
				t.Errorf("Extract:\ngot  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// What the reader must leave out is its own list of cases: a dropped element is dropped
// whole, so nothing it held — text, code, or a link — reaches the result.
func TestExtractDropsScriptsStylesAndNavigation(t *testing.T) {
	source := `<html><head><title>Kept</title><style>body{color:red}</style></head><body>` +
		`<nav><a href="/">home</a></nav>` +
		`<script>var secret = "script text";</script>` +
		`<noscript>enable scripts</noscript>` +
		`<template><p>template text</p></template>` +
		`<svg><text>svg text</text></svg>` +
		`<aside>sidebar</aside><footer>footer text</footer>` +
		`<main><p>the prose</p></main></body></html>`

	got := Extract(source, pageURL(t, "http://example.com/"))
	if !strings.Contains(got, "the prose") {
		t.Fatalf("the reader dropped the prose too: %q", got)
	}
	if !strings.Contains(got, "# Kept") {
		t.Errorf("the title must survive the dropped head: %q", got)
	}
	for _, dropped := range []string{
		"home", "script text", "enable scripts", "template text", "svg text",
		"sidebar", "footer text", "color:red",
	} {
		if strings.Contains(got, dropped) {
			t.Errorf("the reader kept %q, which it must drop: %q", dropped, got)
		}
	}
}

// A link with no base address to resolve against keeps its text: an address that cannot be
// made absolute is not reported as if it were one.
func TestExtractKeepsLinkTextWhenThereIsNoBaseAddress(t *testing.T) {
	got := Extract(`<p><a href="/docs">the docs</a></p>`, nil)
	if want := "the docs\n"; got != want {
		t.Fatalf("Extract:\ngot  %q\nwant %q", got, want)
	}
}

// A reader may be handed anything, including bytes that are not HTML at all: it reads what
// it can and does not panic or invent text.
func TestExtractSurvivesBrokenMarkup(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"an unclosed tag", "<p>text", "text\n"},
		{"a stray close tag", "text</p>", "text\n"},
		{"a comment", "<!-- notes --><p>text</p>", "text\n"},
		{"a doctype", "<!DOCTYPE html><p>text</p>", "text\n"},
		{"an unfinished tag", "<p>text<div class=", "text\n"},
		{"binary-looking bytes", "<p>a\x00b</p>", "a\x00b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Extract(tc.source, pageURL(t, "http://example.com/")); got != tc.want {
				t.Errorf("Extract:\ngot  %q\nwant %q", got, tc.want)
			}
		})
	}
}
