package agent

import _ "embed"

// instruction 只描述通用行为；具体工具用法由工具描述和能力上下文提供。
// 随二进制嵌入，运行时不依赖工作目录中的同名文件。
//
//go:embed prompts/core.md
var instruction string
