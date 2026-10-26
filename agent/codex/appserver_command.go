package codex

import "strings"

// Keep launcher-prefix arguments before the subcommand, but put Codex config
// overrides after it. Otherwise an app-server -c model=... can replace the
// leading override group and silently drop custom MCP configuration.
func appServerCommandArgs(extra []string) []string {
	var prefix, config []string
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		switch {
		case (arg == "-c" || arg == "--config") && i+1 < len(extra):
			config = append(config, arg, extra[i+1])
			i++
		case strings.HasPrefix(arg, "--config="):
			config = append(config, arg)
		default:
			prefix = append(prefix, arg)
		}
	}
	return append(append(prefix, "app-server"), config...)
}
