package web

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Extract 把一份 HTML 取成模型读得懂的正文：<title> 作为首行的一级标题，h1–h6 作为对应
// 级别的 # 行，<li> 作为 "- " 行，链接写成“文字 <绝对地址>”，<pre>/<code> 里的空白原样
// 保留，块与块之间空一行，块外的空白折叠。
//
// 它是启发式的阅读器，不是忠实渲染：script/style/noscript/template/svg/head 以及
// nav/footer/aside 这些导航类容器整棵子树都不读。取舍清单就在下面两张表里，测试逐个
// 钉住它们。base 是这份 HTML 取回来的地址，用来把相对链接解成绝对地址。
func Extract(source string, base *url.URL) string {
	e := &extractor{base: base, leading: true}
	z := html.NewTokenizer(strings.NewReader(source))
	for {
		switch z.Next() {
		case html.ErrorToken:
			// 读到底（或读到坏字节）：已经读到的部分就是这次能给出的正文。
			return e.text()
		case html.TextToken:
			// Token() 交给我们的文字已经由 html 包自己解过实体，不再重复解码。
			e.textToken(z.Token().Data)
		case html.StartTagToken, html.SelfClosingTagToken:
			e.startTag(z.Token())
		case html.EndTagToken:
			e.endTag(z.Token())
		default:
			// 注释、DOCTYPE 之类不是正文。
		}
	}
}

// dropTags 是整棵子树都丢掉的元素：脚本、样式、模板与 head，加上导航类的容器。页面上的
// 每个字不都是正文，而正文是这项工具要取回的东西。
var dropTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "head": true, "nav": true, "footer": true, "aside": true,
}

// blockTags 是当作“块”处理的元素：进出这样一块就在正文里留一个空行。表里没有的元素按行内
// 处理，它们只影响文字之间的空格。这张表同样是启发式的取舍，不是 HTML 的类别定义。
var blockTags = map[string]bool{
	"address": true, "article": true, "blockquote": true, "body": true, "caption": true,
	"dd": true, "details": true, "div": true, "dl": true, "dt": true, "fieldset": true,
	"figcaption": true, "figure": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true, "li": true,
	"main": true, "menu": true, "ol": true, "p": true, "pre": true, "section": true,
	"summary": true, "table": true, "tbody": true, "td": true, "tfoot": true,
	"th": true, "thead": true, "tr": true, "ul": true,
}

// whitespaceRun 是行外文本里要被折叠掉的空白串。
var whitespaceRun = regexp.MustCompile(`[\t\n\v\f\r ]+`)

// noSpaceBefore 是前面不留空格的标点：行内文字被拆成几段时（例如 <a> 里混了 <b>），
// 拼接不能把句号、逗号顶开。
var noSpaceBefore = []string{
	".", ",", ";", ":", "!", "?", ")", "]", "}", "%",
	"。", "，", "、", "；", "：", "！", "？", "）", "」", "』",
}

// extractor 边走一遍 HTML 边把正文写进 body：它记住欠下的空行、行首前缀、要折叠的空白，
// 以及当前是不是在被丢掉的子树里。
type extractor struct {
	// base 是这份 HTML 取回来的地址，用来把相对链接解成绝对地址。
	base *url.URL

	// body 是正文，title 是 <title> 的文字（最后作为首行）。
	body  strings.Builder
	title strings.Builder

	// pending 是下一段文字之前要补的换行数：0 不加、1 换行、2 空行。它只记下要求，落笔在
	// 下一段真正有内容的文字被写进来的时候，所以连续的空块不会堆出成片的空行。
	pending int
	// leading 表示正处在行首：行首不补空格，并且要先把行首前缀写上。
	leading bool
	// space 表示下一段行内文字前要有一个空格。
	space bool
	// lastSpace 表示刚写下的内容以空白结尾，不必再补空格。
	lastSpace bool
	// prefix 是下一个行首的前缀（标题的 # 号、列表项的 - 号），写完一次就清掉。
	prefix string

	// skipTag 非空时整棵子树都被丢掉，skipDeep 是同名标签的嵌套深度。
	skipTag  string
	skipDeep int
	// inTitle 表示正在收 <title> 的文字。
	inTitle bool

	// pre 是 <pre>/<code> 的嵌套层数：里面的空白原样保留。
	pre int

	// anchor 非空时正在收一个 <a> 里的文字，好把文字和它的绝对地址一起写上。
	anchor *anchor
}

// anchor 是一个正在收的链接：href 是解析后的地址（没有可用的地址时是空串），text 是
// 链接里读到的文字。
type anchor struct {
	href  string
	text  strings.Builder
	space bool
}

func (e *extractor) startTag(tag html.Token) {
	name := tag.Data
	// <title> 在 <head> 里面，而 head 整棵是被丢掉的：标题要在这个例外里先被收下来。
	if name == "title" && (e.skipTag == "" || e.skipTag == "head") {
		e.inTitle = true
		return
	}
	if e.inTitle {
		return
	}
	if e.skipTag != "" {
		if name == e.skipTag {
			e.skipDeep++
		}
		return
	}
	if dropTags[name] {
		e.skipTag = name
		e.skipDeep = 0
		return
	}
	switch {
	case headingLevel(name) > 0:
		e.gap(2)
		e.prefix = strings.Repeat("#", headingLevel(name)) + " "
	case name == "li":
		// 列表项各自一行，列表本身（<ul>/<ol>）才是块：一项一行比每项之间都空一行好读。
		e.gap(1)
		e.prefix = "- "
	case name == "pre":
		e.gap(2)
		e.pre++
	case name == "code":
		e.pre++
	case name == "br":
		e.gap(1)
	case name == "a":
		e.openAnchor(tag)
	case blockTags[name]:
		e.gap(2)
	}
}

func (e *extractor) endTag(tag html.Token) {
	name := tag.Data
	if name == "title" && e.inTitle {
		e.inTitle = false
		return
	}
	if e.inTitle {
		return
	}
	if e.skipTag != "" {
		if name == e.skipTag {
			if e.skipDeep == 0 {
				e.skipTag = ""
			} else {
				e.skipDeep--
			}
		}
		return
	}
	switch {
	case headingLevel(name) > 0:
		e.gap(2)
	case name == "li":
		e.gap(1)
	case name == "pre":
		if e.pre > 0 {
			e.pre--
		}
		e.gap(2)
	case name == "code":
		if e.pre > 0 {
			e.pre--
		}
	case name == "a":
		e.closeAnchor()
	case blockTags[name]:
		e.gap(2)
	}
}

func (e *extractor) textToken(text string) {
	if e.inTitle {
		e.title.WriteString(text)
		return
	}
	if e.skipTag != "" {
		return
	}
	if e.pre > 0 {
		e.write(text, true)
		return
	}
	e.write(text, false)
}

// gap 要求下一段文字之前至少空这么多行：2 是一段之间的空行，1 是换行。
func (e *extractor) gap(lines int) {
	if lines > e.pending {
		e.pending = lines
	}
}

// write 把一段文字接进正文。raw 为真（<pre>/<code> 里面）时原样保留，包括换行；否则空白
// 折叠成单个空格，纯空白只记下“这里隔了一段”，真正的落笔在 flush 里。
func (e *extractor) write(text string, raw bool) {
	if raw {
		if text == "" {
			return
		}
		e.flush(text)
		return
	}
	squashed := whitespaceRun.ReplaceAllString(text, " ")
	core := strings.TrimSpace(squashed)
	if core == "" {
		e.setSpace(true)
		return
	}
	if strings.HasPrefix(squashed, " ") {
		e.setSpace(true)
	}
	e.flush(core)
	e.setSpace(strings.HasSuffix(squashed, " "))
}

// flush 落笔：先把欠下的换行补上，再（在行首）写上这一行的前缀，最后按需要补一个空格。
func (e *extractor) flush(text string) {
	if e.pending > 0 {
		if e.body.Len() > 0 {
			e.body.WriteString(strings.Repeat("\n", e.pending))
		}
		e.pending = 0
		e.leading = true
		e.space = false
	}
	if e.leading {
		e.body.WriteString(e.prefix)
		e.prefix = ""
	}
	if e.space && !e.leading && !e.lastSpace {
		e.body.WriteString(" ")
	}
	e.body.WriteString(text)
	e.space = false
	e.lastSpace = strings.HasSuffix(text, " ") || strings.HasSuffix(text, "\n")
	e.leading = strings.HasSuffix(text, "\n")
}

// setSpace 记下“下一段文字前要有一个空格”：正在收链接文字时记在链接上。
func (e *extractor) setSpace(want bool) {
	if e.anchor != nil {
		e.anchor.space = want
		return
	}
	e.space = want
}

// openAnchor 开始收一个链接。<a> 里再套 <a> 时只有外面那层算数，页面上的链接文字就
// 是一段文字。
func (e *extractor) openAnchor(tag html.Token) {
	if e.anchor != nil {
		return
	}
	href := ""
	for _, attr := range tag.Attr {
		if attr.Key == "href" {
			href = attr.Val
			break
		}
	}
	e.anchor = &anchor{href: absoluteHref(e.base, href)}
}

// closeAnchor 把一个链接写成“文字 <绝对地址>”：地址是解析后的绝对地址，解析不出来
// （javascript:、只有片段、没有基准地址……）时就只留文字。
func (e *extractor) closeAnchor() {
	collected := e.anchor
	e.anchor = nil
	if collected == nil {
		return
	}
	label := strings.TrimSpace(collected.text.String())
	switch {
	case label == "" && collected.href == "":
		return
	case collected.href == "":
		e.write(label, false)
	default:
		e.write(label+" <"+collected.href+">", false)
	}
}

// add 把一段行内文字接进链接的文字里：按原文的空格拼接，标点前不补空格。
func (a *anchor) add(squashed string) {
	core := strings.TrimSpace(squashed)
	if core == "" {
		a.space = true
		return
	}
	want := a.text.Len() > 0 && (a.space || strings.HasPrefix(squashed, " ") || !startsWithPunctuation(core))
	if want {
		a.text.WriteString(" ")
	}
	a.text.WriteString(core)
	a.space = strings.HasSuffix(squashed, " ")
}

// startsWithPunctuation 报告一段文字是不是以不该在它前面留空格的标点开头。
func startsWithPunctuation(text string) bool {
	for _, punctuation := range noSpaceBefore {
		if strings.HasPrefix(text, punctuation) {
			return true
		}
	}
	return false
}

// headingLevel 报告一个标签名是不是 h1–h6，是的话给出级别。
func headingLevel(name string) int {
	if len(name) != 2 || name[0] != 'h' || name[1] < '1' || name[1] > '6' {
		return 0
	}
	return int(name[1] - '0')
}

// absoluteHref 把 href 解析成绝对地址。解析不了、指向的不是可读文档（javascript:、
// data:）、只有片段、或者没有基准地址可依时返回空串：那种链接只留文字。
func absoluteHref(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "javascript", "data":
		return ""
	}
	if parsed.Scheme == "" && parsed.Host == "" && parsed.Opaque == "" && parsed.Path == "" {
		return ""
	}
	if base == nil {
		return ""
	}
	absolute := base.ResolveReference(parsed)
	if absolute.Scheme == "" {
		return ""
	}
	return absolute.String()
}

// text 是最终的正文：<title> 作为首行的一级标题，正文跟在空行之后。
func (e *extractor) text() string {
	body := strings.TrimRight(e.body.String(), " \t\n")
	title := strings.TrimSpace(whitespaceRun.ReplaceAllString(e.title.String(), " "))
	switch {
	case title != "" && body != "":
		return "# " + title + "\n\n" + body + "\n"
	case title != "":
		return "# " + title + "\n"
	case body != "":
		return body + "\n"
	default:
		return ""
	}
}
