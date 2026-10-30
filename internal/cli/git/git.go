// Package git groups version-control and local development commands.
package git

import (
	"dyna-agent/internal/cli/change"
	"dyna-agent/internal/cli/ci"
	"dyna-agent/internal/cli/hooks"
	"dyna-agent/internal/cli/pr"
	clisync "dyna-agent/internal/cli/sync"
	"dyna-agent/internal/cli/worktrees"

	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "git", Short: "Manage changes, pull requests, worktrees, hooks and CI", Args: cobra.NoArgs}
	cmd.AddCommand(pr.NewCommand(), pr.NewMergeCommand(), change.NewCommand(), ci.NewCommand(), worktrees.NewCommand(), hooks.NewCommand(), clisync.NewCommand())
	cmd.AddCommand(issueCommand())
	return cmd
}

// issueCommand groups the upstream issue watch commands.
func issueCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "issue", Short: "Watch upstream GitHub issues for a room"}
	cmd.AddCommand(issueWatchCommand(), issueUnwatchCommand())
	return cmd
}
