// Package hook provides the hidden steering hook Dyna injects into
// Codex, Muse, and Claude Code workers it launches. It is an internal transport, not a
// user-installed orchestrator hook.
package hook

import (
	"github.com/spf13/cobra"

	"dyna-agent/internal/harness"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Internal harness hooks",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(steerCmd())
	return cmd
}

func steerCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:    "steer --dir <dir>",
		Short:  "Internal command guard and steering hook for Dyna-launched workers",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if dir == "" {
				return nil
			}
			_ = harness.RunSteerHook(dir, c.InOrStdin(), c.OutOrStdout())
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "the worker run's steering hook handoff directory")
	return c
}
