// Package automations holds the one table of automatic hooks. The engine seeds
// a global hook for each entry, the Hooks screen lists them, and
// `dyna git hooks run <name> --global` runs the same function by hand, so a new
// automatic hook is one table entry and its function.
package automations

import (
	"context"
	"fmt"
	"io"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/subagent"
)

// Hooks returns the automatic hooks of this release. Each entry names a
// built-in, the label the screen shows, its default schedule and the pass it
// runs in the engine process.
func Hooks() []hooks.BuiltinSpec {
	return []hooks.BuiltinSpec{
		{
			Builtin:      "maintenance",
			Label:        "Maintenance",
			EveryMinutes: 60,
			Timeout:      "30m",
			Run:          maintenancePass,
		},
	}
}

// maintenancePass frees disk space and removes stale worktrees, scratch and
// runs. Disk-pressure cleanup stays in the engine loop, which ignores the
// switch on this hook.
func maintenancePass(ctx context.Context, log io.Writer) error {
	_, err := maintenance.Pass(ctx, subagent.NewManager().RetainClosedSession, logger(log))
	return err
}

// logger writes a pass's log lines to the hook run log, one line each.
func logger(out io.Writer) func(string, ...any) {
	return func(format string, args ...any) {
		_, _ = fmt.Fprintf(out, format+"\n", args...)
	}
}
