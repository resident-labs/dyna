package version

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/computers"
)

func CommandExitCode(err error) int {
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		if code := coder.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}

var readBuildInfo = debug.ReadBuildInfo

// buildRevision is a fallback for install builds that omit Go VCS metadata.
var buildRevision string

func Resolve(stamped string) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	info, ok := readBuildInfo()
	revision := buildRevision
	dirty := false
	if ok && info != nil {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					revision = setting.Value
				}
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	result := "dev+" + revision
	if dirty {
		result += "-dirty"
	}
	return result
}

// computersBuild asks the running computer service for its build.
var computersBuild = func(ctx context.Context) (string, error) {
	return computers.NewClient("").Build(ctx)
}

// printComputers says which build the computer service runs, because an update
// restarts it only when no computer is busy.
func printComputers(ctx context.Context, w io.Writer, own string) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	build, err := computersBuild(ctx)
	switch {
	case errors.Is(err, computers.ErrNoDaemon):
		fmt.Fprintln(w, "computers not running")
	case errors.Is(err, computers.ErrUnknownBuild):
		fmt.Fprintln(w, "computers older build (restart pending)")
	case err != nil:
		fmt.Fprintf(w, "computers unknown: %v\n", err)
	case build != own:
		fmt.Fprintf(w, "computers %s (differs from dyna; restart pending)\n", build)
	default:
		fmt.Fprintf(w, "computers %s\n", build)
	}
}

func NewCommand(stamped string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the installed dyna version and the computer service build",
		Run: func(c *cobra.Command, _ []string) {
			own := Resolve(stamped)
			fmt.Fprintf(c.OutOrStdout(), "dyna %s\n", own)
			printComputers(c.Context(), c.OutOrStdout(), own)
		},
	}
}
