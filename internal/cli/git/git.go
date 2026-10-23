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

// TODO: legacy/shim — remove after one release. `dyna git issue` moved to
// top-level `dyna issue`.
func legacyIssueCommand() *cobra.Command {
	cmd := issue.NewCommand()
	cmd.Hidden = true
	return cmd
}

// TODO: legacy/shim — remove after one release. `dyna git export` moved to
// `dyna git pr export`.
func legacyExportCommand() *cobra.Command {
	cmd := cliexport.NewCommand()
	cmd.Hidden = true
	return cmd
}
