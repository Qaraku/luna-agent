package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
	jsonschema "github.com/eino-contrib/jsonschema"
)

// fetchDescription 是模型可见的工具描述。它要说清四件事：取回来的是提取后的文本而不是
// 原版式；结果头部如实报了什么；什么情况下会被拒绝；文本太长时怎么接着读。
const fetchDescription = "Fetch one public http(s) address and return the text of the page or document it answers with. `url` is the address to fetch; it must be http or https, and its host must be, or resolve to, a public address — loopback, private, link-local, unique-local, unspecified and multicast addresses, along with the 6to4 and NAT64 prefixes that carry another address inside them, are refused, both before the request and again when the connection is made, so a name that points inside this machine or its network cannot be reached. HTML is read through a heuristic reader that turns it into text: the title, headings, list items, links (with their absolute address) and preformatted blocks are kept, while scripts, styles and navigation are dropped — what you get is extracted text, not the original layout, so do not present it as the page as it was. A response that is not a text type, a redirect chain longer than a few hops, and an address that does not answer in time are refused, and a refused call returns no text at all. A body longer than 1 MiB is not refused: its first 1 MiB is read, and the result says how much of the body was left unread — text from that part is not in the result, and `start_offset` moves through the text of what was read, so it cannot reach it. The result starts with a header stating which address was actually fetched after redirects, its content-type, how much text was extracted, which byte range of that text this result carries, and whether it was truncated; when the text is longer than one result carries, the header names that limit and the `start_offset` to pass next to read on."

// FetchTool 是 luna_web_fetch：模型把一个公开文档取回来的唯一入口。
//
// 它不持有任何跨调用共享的状态：地址策略、代理判定、上限与时限都来自构造它的那份规则。
type FetchTool struct {
	client *http.Client
	rules  fetchRules
}

// NewFetchTool 建产品用的工具：只放行公开地址，走环境变量里的代理，一次抓取最多
// FetchTimeout。
func NewFetchTool() *FetchTool { return newFetchTool(newClient(productRules), productRules) }

// newFetchTool 是另一个构造入口，只给本包的测试用：它允许换掉整个 http.Client 与规则，
// 好让 httptest 的 127.0.0.1 能被真正取回——产品那份规则对着 loopback 会（正确地）拒绝，
// 而拒绝的原因正是这些测试要单独钉住的东西。产品路径一律走 NewFetchTool()。
func newFetchTool(client *http.Client, rules fetchRules) *FetchTool {
	return &FetchTool{client: client, rules: rules}
}

func (t *FetchTool) Name() string { return FetchToolName }

func (t *FetchTool) Description() string { return fetchDescription }

func (t *FetchTool) Schema() *jsonschema.Schema { return fetchSchema() }

// fetchSchema 是工具的公开 schema：一个必填的地址，加一个可选的起始偏移。
// additionalProperties 关闭，所以未声明的参数在触及网络之前就被拒绝。
func fetchSchema() *jsonschema.Schema {
	type args struct {
		URL         string `json:"url" jsonschema_description:"The http(s) address to fetch; its host must be a public address"`
		StartOffset int    `json:"start_offset,omitempty" jsonschema_description:"The byte offset into the extracted text to start this result at, for reading a long text in parts; 0 by default"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"url"}
	return s
}

// Invoke 取回一个地址。除了这次调用没能完成（超时、网络故障）以外的失败，都是这次调用的
// 拒绝：参数不对、地址不允许、响应不是文本，模型都能自己纠正，因此不带
// plugin.ErrUnavailable 标记。
func (t *FetchTool) Invoke(ctx context.Context, arguments string) (string, error) {
	var in struct {
		URL         string `json:"url"`
		StartOffset int    `json:"start_offset"`
	}
	if err := decodeOne(arguments, &in); err != nil {
		return "", fmt.Errorf("the call is not a single JSON object with the declared parameters: %w", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return "", errors.New("url is required: name the http(s) address to fetch")
	}
	if in.StartOffset < 0 {
		return "", fmt.Errorf("start_offset must be 0 or more (got %d): it is a byte offset into the text a fetch returns", in.StartOffset)
	}
	target, err := parseTarget(in.URL)
	if err != nil {
		return "", err
	}
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: t.Name(), Summary: "fetch a public HTTP(S) resource", Target: target.String(), Permissions: []plugin.AccessKind{plugin.AccessNetwork}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		return "", err
	}
	// 一次抓取的那点时间从地址检查就开始算：解析主机名也可能慢，把它放在预算之外等于
	// 多给了这次调用一段时间。
	requestCtx, cancel := context.WithTimeout(ctx, t.rules.timeout)
	defer cancel()
	if err := checkHost(requestCtx, t.rules.addresses, target.Hostname()); err != nil {
		return "", t.failure(ctx, requestCtx, target, err)
	}
	return t.fetch(ctx, requestCtx, target, in.StartOffset)
}

// parseTarget 只接受 http/https 且带主机名的地址：别的协议、别的形状都不进入请求阶段。
func parseTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not an address this tool can read: %s", raw, err)
	}
	switch target.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("the scheme %q is not fetchable: only http and https addresses can be fetched", target.Scheme)
	}
	if target.Hostname() == "" {
		return nil, fmt.Errorf("%q names no host, so there is nothing to fetch", raw)
	}
	return target, nil
}

// fetch 发这一次请求，并把结果或拒绝渲染给模型。
//
// requestCtx 是这次抓取的预算（Invoke 从地址检查起就开始算），ctx 是它外面的运行：两者
// 分开传，是为了让失败分得清“这次调用超了”和“整个运行被取消了”。
//
// 顺序是有意的：先有地址策略的结论，才有请求；先看清响应是什么类型，才读它的体；超时、
// 重定向越界这类上限到点就拒绝，什么都不返回——半截正文不会被当成抓到了。唯一“读到一半也
// 照常返回”的是响应体超过 1 MiB 的情况，而那种情况会在结果里明说还有多少没有读。
func (t *FetchTool) fetch(ctx, requestCtx context.Context, target *url.URL, offset int) (string, error) {
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%q is not an address this tool can request: %s", target, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", acceptHeader)
	req = markProxy(req, t.rules.proxy)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", t.failure(ctx, requestCtx, target, err)
	}
	defer resp.Body.Close()

	contentType, err := textType(resp)
	if err != nil {
		return "", err
	}
	body, cut, err := readBody(resp)
	if err != nil {
		return "", t.failure(ctx, requestCtx, target, err)
	}
	text, note := textOf(body, resp.Request.URL, contentType)
	return render(page{
		final:       resp.Request.URL,
		requested:   target,
		contentType: contentType,
		note:        note,
		cut:         cut,
		text:        text,
		offset:      offset,
	}), nil
}

// failure 把一次没跑完的抓取折成模型可见的拒绝。顺序是有意的：规则自己下的结论（重定向
// 上限、重定向到了不允许的地址）原样给出；运行被取消（父级 ctx）把整轮交回内核；其次
// 才是这次调用的时限；最后才是别的失败。
func (t *FetchTool) failure(ctx context.Context, requestCtx context.Context, target *url.URL, err error) error {
	var refused *refusal
	if errors.As(err, &refused) {
		return err
	}
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
		return refuse("the fetch of %s did not finish within %s, which is this call's limit on one fetch; nothing is returned", target, shortDuration(t.rules.timeout))
	}
	return refuse("%s could not be fetched: %s", target, cleanErr(err))
}

// textType 只接受文本响应，返回它的 content-type 原文（结果头部照原样报出）。
//
// 判断用媒体类型：text/*，加上 json、xml、javascript、yaml、toml 这些本身就是文本的类型
// 与 +json/+xml 后缀。别的类型（图片、octet-stream、PDF……）一律拒绝，并且把那个类型
// 点名——模型需要知道它撞上的是什么，才能换个地址或换个做法。
func textType(resp *http.Response) (string, error) {
	raw := resp.Header.Get("Content-Type")
	if strings.TrimSpace(raw) == "" {
		return "", refuse("%s answered without a content-type, so this tool cannot tell that the response is text; nothing is returned", resp.Request.URL)
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", refuse("%s answered with a content-type this tool cannot read (%q); nothing is returned", resp.Request.URL, raw)
	}
	if !isTextMediaType(mediaType) {
		return "", refuse("%s answered with the content-type %q, which is not a text type, and this tool fetches text only; nothing is returned", resp.Request.URL, raw)
	}
	return raw, nil
}

// isTextMediaType 报告一个媒体类型是不是这项能力接受的文本类型。
func isTextMediaType(mediaType string) bool {
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-ndjson", "application/yaml", "application/x-yaml", "application/toml":
		return true
	}
	return strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

// readBody 读响应体，最多 MaxBodyBytes。比它更长的响应不是被拒绝，而是读到这里为止，并把
// “还有多少没读”如实记下来：剩下的那部分这次调用拿不到，所以结果里既不能说它不存在，也
// 不能让模型以为用 start_offset 就能接着读——那个偏移在提取后的文本上，越不过没读到的
// 原始字节。
func readBody(resp *http.Response) (body []byte, cut string, err error) {
	read, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(read) > MaxBodyBytes {
		return read[:MaxBodyBytes], bodyCut(resp), nil
	}
	return read, "", nil
}

// bodyCut 写清响应体被上限截住的这件事：读了多少、至少还有多少没有读，以及那部分拿不回来
// 的说明。
func bodyCut(resp *http.Response) string {
	unread := "at least one more byte"
	if resp.ContentLength > MaxBodyBytes {
		unread = fmt.Sprintf("at least %d bytes", resp.ContentLength-MaxBodyBytes)
	}
	return fmt.Sprintf("body: longer than this call's %d-byte limit, so only its first %d bytes were read, leaving %s unread%s; the text below is the text of the bytes that were read, and start_offset reads on through that text and cannot reach the part that was not read",
		MaxBodyBytes, MaxBodyBytes, unread, announced(resp))
}

// announced 在响应自己说了长度、而且那个长度已经超过上限时把它说出来，其余情况什么都不说。
func announced(resp *http.Response) string {
	if resp.ContentLength > MaxBodyBytes {
		return fmt.Sprintf(" (it announced %d bytes)", resp.ContentLength)
	}
	return ""
}

// textOf 把响应体变成要返回的文本。只有 HTML 走启发式阅读器；别的文本类型（text/plain、
// json、xml……）本身已经是文本，原样返回——对它们跑 HTML 阅读器会折叠空白、还会误解码
// 实体，那是改动文档而不是提取正文。
func textOf(body []byte, base *url.URL, contentType string) (text, note string) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = ""
	}
	if isHTMLMediaType(mediaType) {
		return Extract(string(body), base), "extracted from the HTML by this tool's reader, so this is extracted text and not the page as it was laid out"
	}
	return string(body), fmt.Sprintf("taken from the response as it is: %s is already text, so no reader rewrote it", mediaType)
}

// isHTMLMediaType 报告一个媒体类型是不是要用阅读器读的 HTML。
func isHTMLMediaType(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

// page 是一次抓取取回的东西：跟过重定向之后的地址、请求的地址、响应类型、正文，以及正文
// 之外要如实报出的两笔账——正文是怎么来的，和响应体有没有被上限截住。
type page struct {
	// final 是跟过重定向之后真正取到内容的地址，requested 是这次调用要的地址。
	final     *url.URL
	requested *url.URL
	// contentType 是响应声明的类型原文。
	contentType string
	// note 说清文本是怎么来的：HTML 走阅读器，别的文本类型原样取用。
	note string
	// cut 非空时表示响应体比 MaxBodyBytes 长、只读了前一段：它写清还有多少没有读，并说明
	// 那部分拿不回来、start_offset 也到不了。
	cut string
	// text 是提取出来的整份文本，offset 是这次要返回的起始字节。
	text   string
	offset int
}

// render 组装模型可见的结果：先如实报出取到的是什么，再给这一段文本。
//
// 头部逐项报的是：跟过重定向之后真正取到的地址（与请求的地址不同时也把它说出来）、
// content-type、响应体有没有被上限截住、提取出的文本总字节数、本次返回的字节区间，以及
// 是否被截断。截断时说清触到的是哪条上限，并给出继续读的 start_offset。
func render(p page) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fetched %s", p.final)
	if p.final.String() != p.requested.String() {
		fmt.Fprintf(&b, " (the address asked for was %s, and it redirected to this one)", p.requested)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "content-type: %s\n", p.contentType)
	if p.cut != "" {
		b.WriteString(p.cut)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "text: %d bytes, %s\n", len(p.text), p.note)

	switch {
	case len(p.text) == 0:
		b.WriteString("this result carries none of it: there is no text in this response to carry\n")
	case p.offset >= len(p.text):
		fmt.Fprintf(&b, "this result carries 0 of the %d bytes of text: start_offset %d is at or past the end of it, so there is nothing left to read\n", len(p.text), p.offset)
	default:
		end := p.offset + MaxTextBytes
		truncated := end < len(p.text)
		if !truncated {
			end = len(p.text)
		}
		fmt.Fprintf(&b, "this result carries %d of the %d bytes of text, starting at byte %d", end-p.offset, len(p.text), p.offset)
		if truncated {
			fmt.Fprintf(&b, ", truncated at this call's %d-byte limit on how much text one result carries: pass start_offset=%d to read on\n", MaxTextBytes, end)
		} else {
			b.WriteString(", which is the end of it\n")
		}
		b.WriteString("\n")
		b.WriteString(p.text[p.offset:end])
		b.WriteString("\n")
	}
	return b.String()
}

// cleanErr 把客户端包在外面的那层地址重复脱掉（*url.Error 会再念一遍地址），只留下原因，
// 免得拒绝里出现两遍同一句话。
func cleanErr(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

// shortDuration 用毫秒精度渲染时长，避免把 200ms 报成 "200.000123ms"。
func shortDuration(d time.Duration) string { return d.Round(time.Millisecond).String() }

// decodeOne 只接受一个 JSON 对象：拒绝未知字段与尾随的 JSON 值。它是工具自己的一份拷贝，
// 与内核包装器、terminal 和 filewrite 的工具同一条规则：格式不对的调用必须在触及网络
// 之前被拒绝。
func decodeOne(arguments string, into any) error {
	d := json.NewDecoder(strings.NewReader(arguments))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	var extra any
	if extraErr := d.Decode(&extra); extraErr != io.EOF {
		if extraErr == nil {
			return errors.New("expected exactly one JSON object")
		}
		return extraErr
	}
	return nil
}
