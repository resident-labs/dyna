// Package git groups version-control and local development commands.
package git

import (
	"dyna-agent/internal/cli/change"
	"dyna-agent/internal/cli/ci"
	cliexport "dyna-agent/internal/cli/export"
	"dyna-agent/internal/cli/hooks"
	"dyna-agent/internal/cli/issue"
	"dyna-agent/internal/cli/pr"
	clisync "dyna-agent/internal/cli/sync"
	"dyna-agent/internal/cli/worktrees"

	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "git", Short: "Manage changes, pull requests, worktrees, hooks and CI", Args: cobra.NoArgs}
	cmd.AddCommand(pr.NewCommand(), pr.NewMergeCommand(), change.NewCommand(), ci.NewCommand(), worktrees.NewCommand(), hooks.NewCommand(), clisync.NewCommand())
	cmd.AddCommand(legacyIssueCommand(), legacyExportCommand())
	return cmd
}

// `dyna git issue` groups the upstream issue watch commands. Its room-issue
// subcommands moved to top-level `dyna issue` and stay hidden until the shim
// is removed.
// TODO: legacy/shim — remove the hidden subcommands after one release.
func legacyIssueCommand() *cobra.Command {
	cmd := issue.NewCommand()
	cmd.Use = "issue"
	cmd.Short = "Watch upstream GitHub issues for a room"
	cmd.Long = "Watch upstream GitHub issues for a room. Room issues moved to top-level dyna issue."
	for _, sub := range cmd.Commands() {
		sub.Hidden = true
	}
	cmd.AddCommand(issueWatchCommand(), issueUnwatchCommand())
	return cmd
}

// TODO: legacy/shim — remove after one release. `dyna git export` moved to
// `dyna git pr export`.
func legacyExportCommand() *cobra.Command {
	cmd := cliexport.NewCommand()
	cmd.Hidden = true
	return cmd
}
