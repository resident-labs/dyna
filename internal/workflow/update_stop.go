package workflow

import (
	"errors"
	"os"
	"strings"

	"dyna-agent/internal/database"
)

// ErrStoppedForUpdate is the outcome of a run that stopped because a dyna
// update replaced this binary or the state it serves. The coordinator
// finishes the run as interrupted. The author decides whether to resume it,
// because replay could repeat work with side effects (D-402).
var ErrStoppedForUpdate = errors.New("workflow stopped for a dyna update")

// StoppedForUpdate reports whether err is the stop-for-update outcome.
func StoppedForUpdate(err error) bool { return errors.Is(err, ErrStoppedForUpdate) }

// newerSchemaError reports whether err says this binary serves state a newer
// dyna wrote. The store in this process fails with the wrapped sentinel; a
// spawned supervisor reports the sentinel as text across the process boundary.
func newerSchemaError(err error) bool {
	return err != nil && (database.NewerSchema(err) || strings.Contains(err.Error(), database.ErrNewerSchema.Error()))
}

// executableReplaced reports whether an update replaced this process's
// executable. The kernel names a replaced /proc/self/exe target
// "<path> (deleted)", the marker the shell MCP server restart keys on (D-171).
var executableReplaced = func() bool {
	exe, err := os.Readlink("/proc/self/exe")
	return err == nil && strings.HasSuffix(exe, " (deleted)")
}

// updateReplacementCause names the proactive stop's cause.
var updateReplacementCause = errors.New("the dyna binary was replaced by an update")
