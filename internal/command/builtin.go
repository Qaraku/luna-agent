package command

import "github.com/Qaraku/luna-agent/internal/config"

// Builtins are the commands the kernel itself owns.
//
// A capability may contribute its own commands later, the same way it
// contributes tools and context blocks; when it does, they are added at
// composition and pass through the same validation. This function is therefore
// the kernel's own list, not the whole universe of commands.
func Builtins() []Command {
	return []Command{
		ReasoningCommand(config.ReasoningEffortLevels),
		PermissionsCommand(),
		{
			Name:     "help",
			Summary:  "列出 Luna 知道的命令。",
			Usage:    "/help",
			Category: "会话",
			Args:     ArgNone,
			// Listing commands asks nothing of the model or the session, so it
			// works while a run is in flight — which is also when a user is
			// most likely to wonder what else they can type.
			Busy: BusyAllow,
		},
	}
}

// ModelCommand is /model: it shows or chooses which model this session's runs
// are sent to.
//
// The values come from the caller because which models exist is a property of
// the running configuration, not of the command table: the same command means
// different things to two users with different config files, and the table must
// say what this runtime actually offers.
//
// It is refused while a run is active. Changing the model does not touch the run
// that is already in flight — that run keeps the model it started with — so
// accepting the switch would tell the user something happened that did not.
func ModelCommand(models []string) Command {
	options := make([]Option, 0, len(models)+1)
	for _, name := range models {
		options = append(options, Option{Value: name})
	}
	options = append(options, Option{Value: "--default", Summary: "恢复跟随全局默认模型"})
	return Command{
		Name:     "model",
		Summary:  "查看或切换这个会话使用的模型。",
		Usage:    "/model [模型]",
		Category: "模型",
		Args:     ArgOptions,
		Options:  options,
		Busy:     BusyReject,
	}
}

// ReasoningCommand 由服务端声明选项，界面不另写一套档位；none 不等于不发送。
func ReasoningCommand(levels []string) Command {
	options := []Option{{Value: "--default", Summary: "恢复跟随全局设置"}, {Value: "--off", Summary: "不发送 reasoning_effort 字段"}}
	for _, level := range levels {
		options = append(options, Option{Value: level})
	}
	return Command{Name: "reasoning", Summary: "查看或切换这个会话的思考档位。", Usage: "/reasoning [档位 | --default | --off]", Category: "模型", Args: ArgOptions, Options: options, Busy: BusyReject}
}

// PermissionsCommand 只修改用户会话策略；模型输出中的同名文字不能执行它。
func PermissionsCommand() Command {
	options := []Option{{Value: "--default", Summary: "恢复默认：读取允许，其余询问"}}
	for _, kind := range []string{"read", "write", "network", "exec"} {
		for _, decision := range []string{"allow", "ask", "deny"} {
			options = append(options, Option{Value: kind + "=" + decision})
		}
	}
	return Command{Name: "permissions", Summary: "查看或逐项修改会话权限。", Usage: "/permissions [read|write|network|exec=allow|ask|deny | --default]", Category: "权限", Args: ArgOptions, Options: options, Busy: BusyAllow}
}
