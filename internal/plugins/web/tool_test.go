package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// kernelRefusalPrefix 是内核给能力工具拒绝时用的前缀（internal/agent 的 refuseCapability），
// AGENTS.md 把它写死。工具本身返回的是不带前缀的错误——前缀属于内核，在这里再加一次，模型
// 会读到两遍。
const kernelRefusalPrefix = "the tool refused this call: "

// fixture 是一个测试的世界：一个 httptest 服务器（监听 127.0.0.1），和一个为它放宽了地址
// 策略的工具。
//
// 产品那份规则对着 loopback 会（正确地）拒绝，所以端到端测试通过 newFetchTool 注入自己的
// client 与规则——那是这个包唯一的另一个构造入口，它不导出，产品代码只走 NewFetchTool()。
type fixture struct {
	server *httptest.Server
	tool   *FetchTool
}

func newFixture(t *testing.T, handler http.HandlerFunc) *fixture {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &fixture{server: server, tool: permissiveTool(testFetchTimeout)}
}

// permissiveTool 是测试用的工具：两层地址策略都放行、没有代理，时限由调用方给。产品不
// 可能造出这样一份规则，这正是它存在的理由。
func permissiveTool(timeout time.Duration) *FetchTool {
	rules := fetchRules{
		addresses: allowEveryAddress,
		dial:      allowEveryAddress,
		proxy:     noProxy,
		timeout:   timeout,
	}
	return newFetchTool(newClient(rules), rules)
}

// call runs the tool the way the kernel does: one arguments string, one result.
func (f *fixture) call(t *testing.T, arguments string) (string, error) {
	t.Helper()
	return f.tool.Invoke(allowedFetchContext(context.Background()), arguments)
}

// fetch runs the tool against a path on the fixture's server.
func (f *fixture) fetch(t *testing.T, path string) (string, error) {
	t.Helper()
	return f.call(t, args(t, map[string]any{"url": f.server.URL + path}))
}

// args 把一次调用的参数编码成模型会送过来的 JSON。
func args(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// refused 断言一次调用被拒绝：返回错误、带内核对拒绝的语义（不是基础设施故障）、内核加上
// 前缀之后的模型可见文本里带着每一个期望的子串，并且没有把半截结果当成成功返回。
func refused(t *testing.T, result string, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected the tool to refuse this call, result=%q", result)
	}
	if plugin.IsUnavailable(err) {
		t.Fatalf("a call-level refusal must not be marked as an infrastructure failure: %v", err)
	}
	if strings.Contains(err.Error(), kernelRefusalPrefix) {
		t.Fatalf("the tool prefixed its own refusal; the prefix belongs to the kernel: %q", err)
	}
	if result != "" {
		t.Fatalf("a refused call returned a result: %q", result)
	}
	visible := kernelRefusalPrefix + err.Error()
	for _, want := range wants {
		if !strings.Contains(visible, want) {
			t.Fatalf("model-visible refusal %q does not name %q", visible, want)
		}
	}
}

// headerOf splits a result into its header and the text it carries. The header is what a
// caller reads to know what was fetched; the rest is the text itself. A result that carries
// no text is all header — the header still says why there is nothing to hand over.
func headerOf(t *testing.T, result string) (header, text string) {
	t.Helper()
	header, text, ok := strings.Cut(result, "\n\n")
	if !ok {
		return result, ""
	}
	return header, text
}

// The ordinary case: a page is fetched, its text is extracted, and the header says what
// was fetched, what type it was, how much text came out and that all of it is here.
func TestFetchReturnsTheTextOfAPageAndSaysWhatItGot(t *testing.T) {
	const page = `<html><head><title>A page</title></head><body>` +
		`<h1>Heading</h1><p>One <a href="/x">link</a>.</p></body></html>`
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	})

	got, err := f.fetch(t, "/page")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	header, text := headerOf(t, got)
	extracted := "# A page\n\n# Heading\n\nOne link <" + f.server.URL + "/x>.\n"
	for _, want := range []string{
		"fetched " + f.server.URL + "/page",
		"content-type: text/html; charset=utf-8",
		fmt.Sprintf("text: %d bytes", len(extracted)),
		"extracted from the HTML",
		fmt.Sprintf("carries %d of the %d bytes of text", len(extracted), len(extracted)),
		"starting at byte 0",
		"which is the end of it",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q does not say %q", header, want)
		}
	}
	// What follows the header is the text over the reader's output plus the newline the
	// result ends with.
	if want := extracted + "\n"; text != want {
		t.Errorf("text:\ngot  %q\nwant %q", text, want)
	}
}

// A redirect is followed, and the header names both the address that was asked for and the
// one the text really came from.
func TestFetchFollowsRedirectsAndReportsTheAddressItEndedAt(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "the real page\n")
	})

	got, err := f.fetch(t, "/start")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	header, text := headerOf(t, got)
	for _, want := range []string{
		"fetched " + f.server.URL + "/final",
		"the address asked for was " + f.server.URL + "/start",
		"redirected to this one",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q does not say %q", header, want)
		}
	}
	if want := "the real page\n\n"; text != want {
		t.Errorf("text %q, want %q", text, want)
	}
}

// The redirect limit is a limit at both ends: the allowed number of hops is followed, and
// one more is refused with the limit named rather than returned as a shorter fetch.
func TestFetchFollowsAsManyRedirectsAsTheLimitAllowsAndNoMore(t *testing.T) {
	cases := []struct {
		name string
		// hops is how many redirects the server answers before the last one serves content.
		hops int
		want string
	}{
		{name: "no redirect at all", hops: 0},
		{name: "one redirect", hops: 1},
		{name: "exactly the limit", hops: MaxRedirects},
		{name: "one more than the limit", hops: MaxRedirects + 1, want: fmt.Sprintf("more than %d times", MaxRedirects)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				hop, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
				if err != nil || hop >= tc.hops {
					w.Header().Set("Content-Type", "text/plain")
					fmt.Fprintf(w, "arrived after %d hops\n", tc.hops)
					return
				}
				http.Redirect(w, r, fmt.Sprintf("/hop/%d", hop+1), http.StatusFound)
			})
			got, err := f.fetch(t, "/hop/0")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Invoke: %v", err)
				}
				if !strings.Contains(got, fmt.Sprintf("arrived after %d hops", tc.hops)) {
					t.Fatalf("result %q does not carry the final page", got)
				}
				return
			}
			refused(t, got, err, tc.want, "limit", "stopped")
		})
	}
}

// A redirect is a second address, so it is judged like one: the hop check refuses a target
// the hostname policy would have refused had it been asked for directly.
func TestFetchRefusesARedirectToAnAddressThatIsNotAllowed(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/private", http.StatusFound)
	})
	// Loopback is admitted here so the first hop can be reached at all; everything else is
	// judged the way the product judges it.
	f.tool = func() *FetchTool {
		rules := fetchRules{
			addresses: loopbackOnly,
			dial:      allowEveryAddress,
			proxy:     noProxy,
			timeout:   testFetchTimeout,
		}
		return newFetchTool(newClient(rules), rules)
	}()

	got, err := f.fetch(t, "/start")
	refused(t, got, err, "redirected to", "10.0.0.1", "private", "only public addresses")
}

// A body over the size limit is not refused: its first MaxBodyBytes are read and extracted,
// the result says how much of the body was left unread, and it says that start_offset
// cannot reach that part — the offset moves through the text of what was read. A fetch
// that quietly showed the first part as if it were the whole document is the failure this
// case exists to prevent.
func TestFetchReadsUpToTheBodyLimitAndSaysWhatItCouldNotRead(t *testing.T) {
	const over = MaxBodyBytes + 5000
	cases := []struct {
		name string
		// announced sends a content-length over the limit, so the size is known up front;
		// without it the extra bytes only show up while reading.
		announced bool
		want      []string
	}{
		{
			name:      "the response announced a size over the limit",
			announced: true,
			want: []string{
				"at least 5000 bytes",
				fmt.Sprintf("(it announced %d bytes)", over),
			},
		},
		{
			name:      "the size only shows up while reading",
			announced: false,
			want:      []string{"at least one more byte"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				if tc.announced {
					w.Header().Set("Content-Length", strconv.Itoa(over))
				}
				io.WriteString(w, strings.Repeat("x", over))
			})

			got, err := f.fetch(t, "/big")
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			header, text := headerOf(t, got)
			wants := append([]string{
				fmt.Sprintf("body: longer than this call's %d-byte limit", MaxBodyBytes),
				"were read",
				fmt.Sprintf("text: %d bytes", MaxBodyBytes),
				"cannot reach the part that was not read",
				"start_offset",
			}, tc.want...)
			for _, want := range wants {
				if !strings.Contains(header, want) {
					t.Errorf("header %q does not say %q", header, want)
				}
			}
			// The text is the text of the bytes that were read, and one result still only
			// carries its first part — with the limit named, as always.
			carried := strings.TrimSuffix(text, "\n")
			if len(carried) != MaxTextBytes || strings.Trim(carried, "x") != "" {
				t.Errorf("the result carries %d bytes, want the first %d bytes of the body", len(carried), MaxTextBytes)
			}
			if !strings.Contains(header, fmt.Sprintf("%d-byte limit on how much text one result carries", MaxTextBytes)) {
				t.Errorf("header %q does not name the result limit it hit", header)
			}
		})
	}
}

// A fetch that does not finish in time is refused and names the time it was given: a slow
// address is not an address that answered.
func TestFetchStopsAtTheTimeLimit(t *testing.T) {
	release := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, "late")
	})
	// Closing the channel before the server is closed lets the handler return, so the
	// cleanup does not have to wait for a request that is meant never to finish.
	t.Cleanup(func() { close(release) })
	f.tool = permissiveTool(300 * time.Millisecond)

	got, err := f.fetch(t, "/slow")
	refused(t, got, err, "did not finish within 300ms", "limit on one fetch")
}

// The response types this tool reads are pinned on both sides: text types are fetched and
// returned as they are, and anything else is refused with the type named.
func TestFetchReadsTextTypesAndRefusesEverythingElse(t *testing.T) {
	textCases := []struct {
		name        string
		contentType string
		// html says the reader runs over this type rather than the body being taken as is.
		html bool
	}{
		{"plain text", "text/plain", false},
		{"text with a charset", "text/plain; charset=utf-8", false},
		{"csv", "text/csv", false},
		{"html", "text/html", true},
		{"xhtml", "application/xhtml+xml", true},
		{"json", "application/json", false},
		{"a json based type", "application/ld+json", false},
		{"xml", "application/xml", false},
		{"an xml based type", "application/atom+xml", false},
		{"javascript", "application/javascript", false},
		{"yaml", "application/yaml", false},
		{"toml", "application/toml", false},
	}
	for _, tc := range textCases {
		t.Run(tc.name, func(t *testing.T) {
			content := "a &amp; b\n"
			if tc.html {
				content = "<title>T</title><p>a &amp; b</p>"
			}
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				io.WriteString(w, content)
			})
			got, err := f.fetch(t, "/doc")
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			header, text := headerOf(t, got)
			if !strings.Contains(header, "content-type: "+tc.contentType) {
				t.Errorf("header %q does not report the content-type it got", header)
			}
			want := "a &amp; b\n\n"
			if tc.html {
				want = "# T\n\na & b\n\n"
			}
			if text != want {
				t.Errorf("text %q, want %q", text, want)
			}
			if tc.html && !strings.Contains(header, "extracted from the HTML") {
				t.Errorf("header %q does not say the text was extracted", header)
			}
			if !tc.html && !strings.Contains(header, "as it is") {
				t.Errorf("header %q does not say the text was taken as it is", header)
			}
		})
	}

	refusedCases := []struct {
		name        string
		contentType string
		// suppress sends the response without any content-type header at all.
		suppress bool
		want     []string
	}{
		{"an image", "image/png", false, []string{"image/png", "not a text type"}},
		{"a binary stream", "application/octet-stream", false, []string{"application/octet-stream"}},
		{"a pdf", "application/pdf", false, []string{"application/pdf"}},
		{"a content-type that cannot be read", ";;;", false, []string{"cannot read"}},
		{"no content-type at all", "", true, []string{"without a content-type"}},
	}
	for _, tc := range refusedCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.suppress {
					w.Header()["Content-Type"] = nil
				} else {
					w.Header().Set("Content-Type", tc.contentType)
				}
				io.WriteString(w, "whatever")
			})
			got, err := f.fetch(t, "/doc")
			refused(t, got, err, tc.want...)
		})
	}
}

// A text document is returned as it is: the reader is for HTML, and running it over plain
// text would fold the author's whitespace and misread entities.
func TestFetchReturnsAPlainTextDocumentWithoutRewritingIt(t *testing.T) {
	const document = "keep   these  spaces\n\nand &amp; this entity\n\tand a tab\n"
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, document)
	})

	got, err := f.fetch(t, "/notes.txt")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	_, text := headerOf(t, got)
	if want := document + "\n"; text != want {
		t.Errorf("text:\ngot  %q\nwant %q", text, want)
	}
}

// A long text is read in parts: the first result is truncated at the limit, it names the
// offset to continue from, and the parts put back together are the whole text.
func TestFetchReadsALongTextInParts(t *testing.T) {
	var document strings.Builder
	for i := 0; i < 1200; i++ {
		fmt.Fprintf(&document, "line %04d %s\n", i, strings.Repeat(".", 30))
	}
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, document.String())
	})
	whole := document.String()
	if len(whole) <= MaxTextBytes {
		t.Fatalf("the fixture's document is %d bytes, which is not longer than the %d-byte result limit", len(whole), MaxTextBytes)
	}

	offsetPattern := regexp.MustCompile(`pass start_offset=(\d+) to read on`)
	var parts []string
	offset := 0
	for {
		got, err := f.call(t, args(t, map[string]any{"url": f.server.URL + "/long.txt", "start_offset": offset}))
		if err != nil {
			t.Fatalf("Invoke at offset %d: %v", offset, err)
		}
		header, text := headerOf(t, got)
		if want := fmt.Sprintf("text: %d bytes", len(whole)); !strings.Contains(header, want) {
			t.Fatalf("header %q does not report the whole text's size (%q)", header, want)
		}
		part := strings.TrimSuffix(text, "\n")
		parts = append(parts, part)
		if strings.Contains(header, "which is the end of it") {
			if offset+len(part) != len(whole) {
				t.Fatalf("the last part ends at %d, not at the end of the %d-byte text", offset+len(part), len(whole))
			}
			break
		}
		next := offsetPattern.FindStringSubmatch(header)
		if next == nil {
			t.Fatalf("a truncated result does not say where to read on: %q", header)
		}
		if !strings.Contains(header, fmt.Sprintf("%d-byte limit", MaxTextBytes)) {
			t.Fatalf("a truncated result does not name the limit it hit: %q", header)
		}
		parsed, convErr := strconv.Atoi(next[1])
		if convErr != nil {
			t.Fatalf("offset %q: %v", next[1], convErr)
		}
		if want := offset + len(part); parsed != want {
			t.Fatalf("the result says to continue at %d, want %d", parsed, want)
		}
		offset = parsed
		if len(parts) > 10 {
			t.Fatal("reading the text in parts does not end")
		}
	}
	if len(parts) < 2 {
		t.Fatalf("the text was not read in parts at all: %d part(s)", len(parts))
	}
	if joined := strings.Join(parts, ""); joined != whole {
		t.Errorf("the parts put back together are not the whole text:\ngot  %d bytes\nwant %d bytes", len(joined), len(whole))
	}
}

// An offset past the end of the text is not a failure: it is a request for a part that is
// not there, and the answer says so instead of showing something from somewhere else.
func TestFetchSaysThereIsNothingLeftWhenTheOffsetIsPastTheEnd(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "short text\n")
	})
	got, err := f.call(t, args(t, map[string]any{"url": f.server.URL + "/x", "start_offset": 9000}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	header, _ := headerOf(t, got)
	for _, want := range []string{"carries 0 of the 11 bytes", "start_offset 9000", "nothing left to read"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q does not say %q", header, want)
		}
	}
}

// An address that is not http or https never reaches the network, and the refusal says
// which scheme it was.
func TestFetchRefusesAnAddressThatIsNotHttpOrHttps(t *testing.T) {
	tool := NewFetchTool()
	cases := []struct {
		name string
		url  string
		want []string
	}{
		{"ftp", "ftp://example.com/x", []string{"ftp", "only http and https"}},
		{"a file url", "file:///etc/passwd", []string{"file", "only http and https"}},
		{"a websocket url", "ws://example.com/x", []string{"ws", "only http and https"}},
		{"a relative path", "/etc/passwd", []string{"only http and https"}},
		{"a data url", "data:text/plain,hi", []string{"only http and https"}},
		{"an address that does not parse", "http://exa mple.com/", []string{"is not an address this tool can read"}},
		{"an http address with no host", "http://", []string{"names no host"}},
		{"an https address with no host", "https://", []string{"names no host"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tool.Invoke(allowedFetchContext(context.Background()), args(t, map[string]any{"url": tc.url}))
			refused(t, got, err, tc.want...)
		})
	}
}

// An address is required: an empty one, or none at all, is refused before anything else.
func TestFetchRefusesAnEmptyOrMissingUrl(t *testing.T) {
	tool := NewFetchTool()
	for _, arguments := range []string{
		`{"url":""}`,
		`{"url":"   "}`,
		`{}`,
	} {
		got, err := tool.Invoke(allowedFetchContext(context.Background()), arguments)
		refused(t, got, err, "url is required")
	}
}

// A malformed call is refused before the network is touched at all.
func TestFetchRefusesArgumentsThatAreNotOneJsonObject(t *testing.T) {
	touched := false
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { touched = true })
	for _, arguments := range []string{
		`not json`,
		`{"url":"a"}{"url":"b"}`,
		`{"url":"` + f.server.URL + `","unknown":1}`,
		``,
		`{"url":` + fmt.Sprint(1) + `}`,
	} {
		got, err := f.call(t, arguments)
		refused(t, got, err, "JSON object")
	}
	if touched {
		t.Fatal("a malformed call reached the server")
	}
}

// A negative offset is refused before anything is fetched: it is not a place in a text.
func TestFetchRefusesANegativeStartOffset(t *testing.T) {
	touched := false
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { touched = true })
	got, err := f.call(t, `{"url":"`+f.server.URL+`/x","start_offset":-1}`)
	refused(t, got, err, "start_offset must be 0 or more", "-1")
	if touched {
		t.Fatal("a call with a negative offset reached the server")
	}
}

// The request this tool sends is part of what the capability promises: a GET, naming
// itself in the User-Agent, and asking for HTML first.
func TestFetchAsksWithTheShapeTheCapabilityPromises(t *testing.T) {
	var method, agent, accept string
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		method, agent, accept = r.Method, r.Header.Get("User-Agent"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok\n")
	})
	if _, err := f.fetch(t, "/x"); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if method != http.MethodGet {
		t.Errorf("method = %q, want GET", method)
	}
	if agent != userAgent || userAgent != "luna-agent" {
		t.Errorf("User-Agent = %q, want %q", agent, userAgent)
	}
	if !strings.HasPrefix(accept, "text/html") {
		t.Errorf("Accept = %q, want it to prefer text/html", accept)
	}
}

// The limits are the ones the capability is defined by: a silent change to any of them is a
// change to what the tool promises, so they are pinned here.
func TestTheLimitsAreTheOnesTheCapabilityIsDefinedBy(t *testing.T) {
	tests := []struct {
		name string
		got  int64
		want int64
	}{
		{"the time one fetch gets", int64(FetchTimeout), int64(20 * time.Second)},
		{"the body one fetch reads", int64(MaxBodyBytes), int64(1 << 20)},
		{"the text one result carries", int64(MaxTextBytes), int64(16 << 10)},
		{"the redirects one call follows", int64(MaxRedirects), int64(5)},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// The tool's public shape: the name the model calls, one required parameter, one optional
// one, no undeclared parameters, and a description that says what the model needs to know.
func TestFetchToolSchemaAndDescriptionShape(t *testing.T) {
	tool := NewFetchTool()
	if tool.Name() != FetchToolName || FetchToolName != "luna_web_fetch" {
		t.Fatalf("tool name = %q", tool.Name())
	}

	encoded, err := json.Marshal(tool.Schema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["type"] != "object" || raw["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %s", encoded)
	}
	required, ok := raw["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "url" {
		t.Fatalf("required = %v, want exactly [url]", raw["required"])
	}
	properties, ok := raw["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", raw["properties"])
	}
	for _, name := range []string{"url", "start_offset"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("schema has no %q parameter: %s", name, encoded)
		}
	}
	if len(properties) != 2 {
		t.Fatalf("the tool exposes an undeclared parameter: %s", encoded)
	}
	if startOffset, _ := properties["start_offset"].(map[string]any); startOffset["type"] != "integer" {
		t.Fatalf("start_offset = %v, want an integer", properties["start_offset"])
	}

	text := tool.Description()
	for _, want := range []string{
		"http(s)",
		"public address",
		"loopback",
		"6to4",
		"NAT64",
		"heuristic reader",
		"extracted text",
		"content-type",
		"start_offset",
		"truncated",
		"refused",
		"1 MiB",
		"unread",
		"cannot reach",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the description must say %q: %q", want, text)
		}
	}
}

// The descriptor is the capability's whole declaration: one tool, the network permission,
// and no claim on anything the capability does not provide.
func TestTheDescriptorDeclaresOneToolAndTheFetchPermission(t *testing.T) {
	d := Descriptor()
	if d.ID != PluginID || PluginID != "web" {
		t.Fatalf("id = %q", d.ID)
	}
	if d.Title != PluginTitle || PluginTitle != "联网" {
		t.Fatalf("title = %q", d.Title)
	}
	if d.Deployment != plugin.DeploymentBuiltin {
		t.Fatalf("deployment = %q", d.Deployment)
	}
	if len(d.Claims) != 0 {
		t.Fatalf("the web capability must claim nothing: %+v", d.Claims)
	}
	if len(d.Contributions) != 1 || d.Contributions[0].Kind != plugin.ContributionTool || d.Contributions[0].ID != FetchToolName {
		t.Fatalf("contributions = %+v", d.Contributions)
	}
	if len(d.Permissions) != 1 || d.Permissions[0].Kind != plugin.PermissionNetworkFetch || d.Permissions[0].Detail != "" {
		t.Fatalf("permissions = %+v", d.Permissions)
	}
}

// The exposed tools are exactly the tools the descriptor declares.
func TestTheToolsAreBoundToThePluginDescriptor(t *testing.T) {
	p := New()
	declared := map[string]bool{}
	for _, c := range p.Descriptor().Contributions {
		if c.Kind == plugin.ContributionTool {
			declared[c.ID] = true
		}
	}
	tools := p.Tools()
	if len(tools) != len(declared) {
		t.Fatalf("the plugin exposes %d tools, the descriptor declares %d", len(tools), len(declared))
	}
	for _, tool := range tools {
		if !declared[tool.Name()] {
			t.Fatalf("the plugin exposes %q, which the descriptor does not declare", tool.Name())
		}
	}
	if p.Descriptor().ID != Descriptor().ID {
		t.Fatal("the plugin's descriptor and the package's Descriptor() disagree")
	}
}

func allowedFetchContext(ctx context.Context) context.Context {
	p := plugin.DefaultAccessPolicy()
	p.Network = plugin.DecisionAllow
	return plugin.WithRun(ctx, plugin.RunInfo{Permissions: &p})
}
