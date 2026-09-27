package command

// Builtins are the commands the kernel itself owns.
//
// A capability may contribute its own commands later, the same way it
// contributes tools and context blocks; when it does, they are added at
// composition and pass through the same validation. This function is therefore
// the kernel's own list, not the whole universe of commands.
func Builtins() []Command {
	return []Command{
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
