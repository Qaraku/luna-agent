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

// ContextKind 是上下文贡献的语义类型。本轮只实现 ContextReference 的渲染；
// 其它类型的贡献会被拒绝并报明确错误——这是“尚未实现”，不是永久禁止。
type ContextKind string

const (
	// ContextReference 是参考数据，绝不是指令。
	ContextReference ContextKind = "reference"
	// ContextInstruction 是项目规则等（未实现）。
	ContextInstruction ContextKind = "instruction"
	// ContextSkill 是程序性知识（未实现）。
	ContextSkill ContextKind = "skill"
)

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
