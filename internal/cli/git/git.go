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
	cmd.AddCommand(pr.NewCommand(), pr.NewMergeCommand(), change.NewCommand(), ci.NewCommand(), worktrees.NewCommand(), hooks.NewCommand(), clisync.NewCommand(), cliexport.NewCommand())
	cmd.AddCommand(issue.NewCommand())
	return cmd
}
