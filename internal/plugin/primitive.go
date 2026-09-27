package plugin

import (
	"context"
	"net/http"

	jsonschema "github.com/eino-contrib/jsonschema"
)

// Tool 是模型可调用的动作。Description 与 Schema 都是模型可见内容，由插件拥有。
type Tool interface {
	Name() string
	Description() string
	Schema() *jsonschema.Schema
	// Invoke 返回模型可见结果。返回 error 时由 Kernel 分类：基础设施故障结束整轮，
	// 其它错误作为“工具拒绝本次调用”的结果返回模型。
	Invoke(ctx context.Context, arguments string) (string, error)
}

// ContextKind 是上下文贡献的语义类型。合法取值集中在 Valid 一处：注册期用它拒绝
// 未知取值——一个内核不认识的 Kind 既不能被诚实标注，也不能被合理预算，让它在装配
// 时退化成一条没有语义的字符串就是在说谎。
//
// 本包只定义“有哪些 Kind 以及它们是否合法”；每种 Kind 对模型意味着什么由装配方
// （internal/agent）决定，业务语义不写进 Kernel。
type ContextKind string

const (
	// ContextReference 是参考数据，绝不是指令。
	ContextReference ContextKind = "reference"
	// ContextInstruction 是项目规则：模型应当遵循，但不能改写系统指令。
	ContextInstruction ContextKind = "instruction"
	// ContextSkill 是程序性知识（尚未渲染）。
	ContextSkill ContextKind = "skill"
)

// Valid 报告 k 是不是内核认识的一种上下文类型。它是“合法 Kind 有哪些”的唯一来源：
// 注册期与装配方都据此判定，新增 Kind 只改这一处。
func (k ContextKind) Valid() bool {
	switch k {
	case ContextReference, ContextInstruction, ContextSkill:
		return true
	default:
		return false
	}
}

// ContextBlock 是一段被贡献的上下文。文本由插件自己渲染好，Kernel 负责标注与预算。
type ContextBlock struct {
	ID   string
	Kind ContextKind
	Text string
}

// Route 是插件贡献的 HTTP 入口。Kernel 在调用前已做 Host/Origin/体积校验。
type Route interface {
	Method() string
	Path() string
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// Panel 是插件贡献的浏览器面板：宿主提供容器与开关，Entry 指向插件自己提供的同源模块。
type Panel struct {
	ID    string
	Title string
	Entry string
}
