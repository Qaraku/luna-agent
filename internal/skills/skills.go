// Package skills 发现用户机器上的 skill 目录，并把它们渲染成一份可以被注入的清单。
//
// 一个 skill 是一个目录，目录里有一个 `SKILL.md`：文件开头的 YAML frontmatter 说明这
// 个 skill 叫什么、什么时候有用，正文说明怎么做某类事。本包只做**一层**发现（不处理
// 分类子目录），也只读 frontmatter——正文是渐进披露的第二级，由读取它的工具按需取回。
//
// 发现不要求任何权限：根由装配层给出（用户级目录或命令行开关），本包只读自己被发现
// 的那个根，不解析第二套边界。校验失败、无法读取、同名被遮住都作为 `Problem` 返回，
// 调用方决定怎么报告；没有一种情况会 panic 或让启动失败。
package skills

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Scope 说明一个 skill 根属于谁。优先级是 builtin < user < project：越靠近当前
// 工作的那个来源越具体，同名时它胜出。
type Scope string

const (
	// ScopeBuiltin 是随 Luna 一起发布的 skill。
	ScopeBuiltin Scope = "builtin"
	// ScopeUser 是用户自己安装的 skill。
	ScopeUser Scope = "user"
	// ScopeProject 属于当前工作的项目。
	ScopeProject Scope = "project"
)

// rank 是优先级的唯一来源：数值大者胜出。未知来源按最低处理，因为它不可能是
// 调用方有意声明的更具体来源。
func (s Scope) rank() int {
	switch s {
	case ScopeProject:
		return 2
	case ScopeUser:
		return 1
	default:
		return 0
	}
}

const (
	// MaxNameBytes 是 skill 名字的长度上限，与 Claude Code / Codex 一致。
	MaxNameBytes = 64
	// MaxDescriptionChars 是 description 的字符数上限（按 rune 计），与
	// Claude Code / Codex 的 1024 一致。
	MaxDescriptionChars = 1024
	// DefaultListBudget 是清单的字节预算。它比内核给这条上下文贡献的预算小，
	// 差额留给清单自己的说明行与内核加的标注，清单因此不会被内核二次截断。
	DefaultListBudget = 8000

	// FileName is the file that makes a directory a skill. It is exported
	// because the tool that reads a skill by name resolves exactly this file
	// when no path is given, and two spellings of it could drift apart.
	FileName = "SKILL.md"

	// MaxSkillFileBytes bounds the file discovery reads for one skill. Only the
	// frontmatter is needed, but a file is read as a whole or not at all: one
	// over this size is reported rather than loaded, and never truncated — the
	// same rule the file tools follow. The number is the file tools' own
	// single-read cap, so one size answers "is this a text file Luna reads".
	MaxSkillFileBytes = 256 * 1024
)

// namePattern 是名字的字符规则：小写字母开头，之后是小写字母、数字与连字符。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Root 是一个被扫描的 skill 根目录。
type Root struct {
	// Path 是根的路径，由调用方给出。
	Path string
	// Scope 是这个根里所有 skill 的来源。
	Scope Scope
}

// Skill 是一个被发现并通过校验的 skill。它只带发现期读到的信息，不含正文。
type Skill struct {
	Name        string
	Description string
	// Dir 是 skill 自己的目录。读取工具以它为边界，因此模型给的相对路径永远
	// 走不出这一个 skill。
	Dir   string
	Scope Scope
	// Revision 只用于受管理的不可变目录；普通用户目录为空。
	Revision string
	Owner    string
}

// Problem 是一次发现里需要报告的一件事：一个被拒绝的 SKILL.md、一个被遮住的同名
// skill。它不是错误：其余 skill 照常被发现。
type Problem struct {
	// Path 是出问题的文件路径。
	Path string
	// Reason 说明它为什么被拒绝或被遮住。
	Reason string
}

// String 让问题可以直接写进一行日志。
func (p Problem) String() string { return p.Path + ": " + p.Reason }

// frontmatter 是本包认识的 frontmatter 字段。格式里定义的字段都列在这里，这样一个
// 为其他客户端写的文件也能被读懂；本包目前只用 name 与 description。
//
// 未知字段被忽略，而不是当作错误：skill 是第三方内容，格式本身比 Luna 认识的字段
// 多，因为多写了一个字段就拒绝一份可用的 skill，会让这个格式比它实际的样子更不可
// 移植。yaml.v3 的默认行为正好是忽略未知字段，所以这条取舍不需要额外代码，只需要
// 不打开 KnownFields(true)。
type frontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license"`
	Compatibility string         `yaml:"compatibility"`
	Metadata      map[string]any `yaml:"metadata"`
	AllowedTools  any            `yaml:"allowed-tools"`
	Version       string         `yaml:"version"`
	Category      string         `yaml:"category"`
	Tags          any            `yaml:"tags"`
}

// Discover 扫描每一个根，返回通过校验的 skill 与需要报告的问题。
//
// 找到的东西按根的顺序、每个根内按目录名的顺序返回，所以同一组根两次扫描给出同一
// 份清单。
//
// 一个根不存在、不是目录、或无法列出时，它只是没有 skill，不产生问题：这些是用户
// 还没放东西的常见状态，不是需要修的故障。相反，一个存在却读不了的 `SKILL.md` 会
// 被报告出来，不会被静默跳过。
//
// 同名冲突按 builtin < user < project 取优先级高的那个，被遮住的那个作为问题报告
// 出去。既不静默合并（两个正文不同的 skill 合成一个是在编造），也不静默丢弃（用户
// 需要知道它的文件没有生效）。
//
// 目录项按 ReadDir 的信息判断是不是目录，因此指向目录的符号链接不会被跟进：skill
// 的边界止于它被发现的根。
func Discover(roots []Root) ([]Skill, []Problem) {
	var found []Skill
	var problems []Problem
	seen := make(map[string]int) // skill 名字 -> found 里的下标
	for _, root := range roots {
		entries, err := os.ReadDir(root.Path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root.Path, entry.Name())
			file := filepath.Join(dir, FileName)
			raw, err := readBounded(file)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// 一个没有 SKILL.md 的目录不是 skill，也就是没有发现
					// 任何东西。
					continue
				}
				problems = append(problems, Problem{Path: file, Reason: "cannot be read: " + err.Error()})
				continue
			}
			skill, err := parse(entry.Name(), dir, raw)
			if err != nil {
				problems = append(problems, Problem{Path: file, Reason: err.Error()})
				continue
			}
			skill.Scope = root.Scope
			index, exists := seen[skill.Name]
			if !exists {
				seen[skill.Name] = len(found)
				found = append(found, skill)
				continue
			}
			kept := found[index]
			if kept.Scope.rank() >= skill.Scope.rank() {
				problems = append(problems, Problem{Path: file, Reason: fmt.Sprintf("shadowed by the %s skill of the same name at %s", kept.Scope, filepath.Join(kept.Dir, FileName))})
				continue
			}
			problems = append(problems, Problem{Path: filepath.Join(kept.Dir, FileName), Reason: fmt.Sprintf("shadowed by the %s skill of the same name at %s", skill.Scope, file)})
			found[index] = skill
		}
	}
	return found, problems
}

// readBounded reads one SKILL.md up to MaxSkillFileBytes and refuses anything
// larger instead of loading it. It reads the cap plus one byte, so a file that
// grows between the size check and the read is still refused rather than half
// read; the caller turns any error into a reported problem.
func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > MaxSkillFileBytes {
		return nil, fmt.Errorf("is %d bytes, over the %d-byte limit", info.Size(), MaxSkillFileBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxSkillFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSkillFileBytes {
		return nil, fmt.Errorf("is over the %d-byte limit", MaxSkillFileBytes)
	}
	return raw, nil
}

// parse 把一个 SKILL.md 读成一个 skill：切出 frontmatter、解析它、校验它。它不读
// 正文——那是读取工具的事。
func parse(dirName, dir string, raw []byte) (Skill, error) {
	front, _, ok := splitFrontmatter(string(raw))
	if !ok {
		return Skill{}, errors.New("the file has no frontmatter block")
	}
	var header frontmatter
	if err := yaml.Unmarshal([]byte(front), &header); err != nil {
		return Skill{}, fmt.Errorf("the frontmatter is not valid YAML: %w", err)
	}
	if err := validate(dirName, header); err != nil {
		return Skill{}, err
	}
	return Skill{Name: header.Name, Description: strings.TrimSpace(header.Description), Dir: dir}, nil
}

// validate 是三处校验的唯一实现：名字与目录一致、名字合法、description 在场且不
// 超长。每一条拒绝都说明发生了什么，理由是给用户看的，不是给日志分类的。
func validate(dirName string, header frontmatter) error {
	switch name := header.Name; {
	case name == "":
		return errors.New("the frontmatter has no name")
	case name != dirName:
		return fmt.Errorf("the name %q does not match the directory name %q", name, dirName)
	case len(name) > MaxNameBytes:
		return fmt.Errorf("the name is %d bytes, over the %d-byte limit", len(name), MaxNameBytes)
	case !namePattern.MatchString(name):
		return fmt.Errorf("the name %q is not lowercase letters, digits and dashes", name)
	}
	description := strings.TrimSpace(header.Description)
	if description == "" {
		return errors.New("the frontmatter has no description")
	}
	if n := utf8.RuneCountInString(description); n > MaxDescriptionChars {
		return fmt.Errorf("the description is %d characters, over the %d-character limit", n, MaxDescriptionChars)
	}
	return nil
}

// splitFrontmatter 把一个 SKILL.md 切成 frontmatter 与正文。ok 为 false 表示文件
// 没有以 frontmatter 块开头；块没有闭合（缺结束分隔行）也算没有。正文是结束分隔行
// 之后的全部内容，正文里出现的其它 `---` 不再特殊。
func splitFrontmatter(raw string) (front, body string, ok bool) {
	lines := strings.SplitAfter(raw, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != "---" {
		return "", "", false
	}
	var header strings.Builder
	for i := 1; i < len(lines); i++ {
		switch strings.TrimRight(lines[i], "\r\n") {
		case "---", "...":
			return header.String(), strings.Join(lines[i+1:], ""), true
		}
		header.WriteString(lines[i])
	}
	return "", "", false
}

// Body 返回一份 SKILL.md 的正文：frontmatter 块之后的全部内容。读一个 skill 时给
// 模型看的就是它——文件开头的字段是给发现用的，读正文的人不需要再看一遍。没有
// frontmatter 块的文件，整份都是正文。
func Body(raw string) string {
	_, body, ok := splitFrontmatter(raw)
	if !ok {
		return raw
	}
	return body
}

// heading 是清单里一行不带 description 的形式：名字与来源。名字是清单里唯一不能
// 少的东西——读取工具按它找 skill，预算不足时先丢 description。
func (s Skill) heading() string {
	return "- " + s.Name + " (" + string(s.Scope) + ")"
}

// Line 是清单里的一行：名字、来源与 description。它只有一层，没有缩进也没有宿主
// 路径——skill 的目录是用户的机器上的事实，不是模型需要的东西。
func (s Skill) Line() string {
	description := strings.TrimSpace(s.Description)
	if description == "" {
		return s.heading()
	}
	return s.heading() + " — " + description
}

// List 把清单渲染成一个可注入的文本块。第二个返回值报告这份文本是不是因为预算被
// 裁过。
//
// 裁减顺序是固定的：先丢掉全部 description，再按顺序少列后面的 skill，最后只剩一
// 行说明。任何一步被触发，文本里都会明说触到了上限并给出被列出的数量——静默截断
// 会让模型以为自己看到了全部 skill，而按名字找一个不存在的 skill 是它无法察觉的
// 错误。没有 skill 时返回空字符串，调用方据此不贡献这一块。
func List(found []Skill, budget int) (string, bool) {
	if len(found) == 0 {
		return "", false
	}
	if budget <= 0 {
		budget = DefaultListBudget
	}
	full := make([]string, 0, len(found))
	names := make([]string, 0, len(found))
	for _, skill := range found {
		full = append(full, skill.Line())
		names = append(names, skill.heading())
	}
	if joined := strings.Join(full, "\n"); len(joined) <= budget {
		return joined, false
	}
	notice := func(shown int) string {
		return fmt.Sprintf("(this list hit its %d-byte limit: %d of %d skills are listed, without their descriptions)", budget, shown, len(found))
	}
	for kept := len(names); kept > 0; kept-- {
		text := strings.Join(names[:kept], "\n")
		trailer := notice(kept)
		if len(text)+1+len(trailer) <= budget {
			return text + "\n" + trailer, true
		}
	}
	// 连一个名字都放不下：只剩说明这一条诚实的答案；预算再小到放不下它，就只能
	// 报告一次空裁减，而不是假装清单是完整的。
	if trailer := notice(0); len(trailer) <= budget {
		return trailer, true
	}
	return "", true
}

// ParseDefinition 校验已经由可信来源读取的技能定义，复用普通目录发现的规则。
func ParseDefinition(name, dir string, raw []byte) (Skill, error) {
	if len(raw) > MaxSkillFileBytes || !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return Skill{}, errors.New("invalid or oversized skill text")
	}
	return parse(name, dir, raw)
}
