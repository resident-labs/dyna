package harness

import (
	"strings"
)

func claudeHasExplicitPermissions(args []string) bool {
	return hasAnyOption(args,
		"--permission-mode", "--allowedTools", "--allowed-tools",
		"--disallowedTools", "--disallowed-tools",
		"--dangerously-skip-permissions",
	)
}

func hasOptionValue(args []string, option, want string) bool {
	for i, arg := range args {
		switch {
		case arg == option:
			for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "-"); j++ {
				if optionValueContains(args[j], want) {
					return true
				}
			}
		case strings.HasPrefix(arg, option+"="):
			if optionValueContains(strings.TrimPrefix(arg, option+"="), want) {
				return true
			}
		}
	}
	return false
}

func optionValueContains(value, want string) bool {
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		if item == want || strings.HasPrefix(item, want+"(") {
			return true
		}
	}
	return false
}
