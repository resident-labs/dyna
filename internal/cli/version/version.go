package version

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
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

func NewCommand(stamped string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the installed dyna version",
		Run: func(c *cobra.Command, _ []string) {
			fmt.Fprintf(c.OutOrStdout(), "dyna %s\n", Resolve(stamped))
		},
	}
}
