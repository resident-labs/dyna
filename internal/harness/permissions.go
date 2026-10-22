package harness

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/profile"
)

// withJournalPermissions converts an explicitly read-only Codex profile into
// a narrow custom permission profile: the target workspace stays read-only,
// while only the run-owned agent directory (which contains journal.jsonl)
// is writable.
// Unlike --sandbox, these -c overrides are accepted by exec resume, so
// exact-session journal reminders remain available.
func withJournalPermissions(p profile.Profile, journalPath string) (profile.Profile, error) {
	if p.Harness != profile.HarnessCodex || journalPath == "" {
		return p, nil
	}
	extra, readOnly := stripCodexReadOnlySandbox(p.ExtraArgs)
	if !readOnly {
		return p, nil
	}
	if !filepath.IsAbs(journalPath) || filepath.Clean(journalPath) != journalPath {
		return p, fmt.Errorf("codex journal path must be canonical and absolute")
	}
	if hasArg(extra, "--dangerously-bypass-approvals-and-sandbox") {
		return p, fmt.Errorf("codex profile combines a read-only sandbox with --dangerously-bypass-approvals-and-sandbox")
	}

	journalDir := filepath.Dir(journalPath)
	permissionConfig := fmt.Sprintf(`permissions.dyna-journal={extends=":read-only",filesystem={%s="write"}}`, strconv.Quote(journalDir))
	extra = append(extra,
		"-c", `default_permissions="dyna-journal"`,
		"-c", `approval_policy="never"`,
		"-c", permissionConfig,
	)
	p.ExtraArgs = extra
	p.SafeMode = true
	return p, nil
}

func stripCodexReadOnlySandbox(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	readOnly := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--sandbox" || arg == "-s") && i+1 < len(args) && strings.EqualFold(strings.Trim(args[i+1], `"'`), "read-only") {
			readOnly = true
			i++
			continue
		}
		if strings.EqualFold(arg, "--sandbox=read-only") || strings.EqualFold(arg, "-s=read-only") {
			readOnly = true
			continue
		}
		if (arg == "-c" || arg == "--config") && i+1 < len(args) && codexConfigSetsReadOnly(args[i+1]) {
			readOnly = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "-c=") && codexConfigSetsReadOnly(strings.TrimPrefix(arg, "-c=")) ||
			strings.HasPrefix(arg, "--config=") && codexConfigSetsReadOnly(strings.TrimPrefix(arg, "--config=")) {
			readOnly = true
			continue
		}
		out = append(out, arg)
	}
	return out, readOnly
}

func codexConfigSetsReadOnly(config string) bool {
	key, value, ok := strings.Cut(config, "=")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if key == "sandbox_mode" {
		return strings.EqualFold(value, "read-only")
	}
	return key == "default_permissions" &&
		(strings.EqualFold(value, ":read-only") || strings.EqualFold(value, "read-only"))
}

func codexHasExplicitPermissions(args []string) bool {
	if hasAnyOption(args,
		"--sandbox", "-s", "--profile", "-p",
		"--dangerously-bypass-approvals-and-sandbox",
	) {
		return true
	}
	for _, config := range codexConfigArgs(args) {
		key, _, ok := strings.Cut(config, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "sandbox_mode" || key == "sandbox_permissions" || key == "default_permissions" ||
			key == "permissions" || strings.HasPrefix(key, "permissions.") {
			return true
		}
	}
	return false
}

func codexConfigArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "-c" || args[i] == "--config") && i+1 < len(args):
			out = append(out, args[i+1])
			i++
		case strings.HasPrefix(args[i], "-c="):
			out = append(out, strings.TrimPrefix(args[i], "-c="))
		case strings.HasPrefix(args[i], "--config="):
			out = append(out, strings.TrimPrefix(args[i], "--config="))
		}
	}
	return out
}

func claudeHasExplicitPermissions(args []string) bool {
	return hasAnyOption(args,
		"--permission-mode", "--allowedTools", "--allowed-tools",
		"--disallowedTools", "--disallowed-tools",
		"--dangerously-skip-permissions",
	)
}

func museHasExplicitPermissions(args []string) bool {
	return hasAnyOption(args,
		"--yolo", "--disable-approval", "--approval-mode", "--permission-profile",
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

func codexExplicitlyEnablesMultiAgent(args []string) bool {
	if hasOptionValue(args, "--enable", "multi_agent") {
		return true
	}
	for _, config := range codexConfigArgs(args) {
		key, value, ok := strings.Cut(config, "=")
		if ok && strings.TrimSpace(key) == "features.multi_agent" && !strings.EqualFold(strings.Trim(strings.TrimSpace(value), "\"'"), "false") {
			return true
		}
	}
	return false
}
