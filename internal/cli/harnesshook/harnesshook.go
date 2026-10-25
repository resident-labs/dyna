// Package harnesshook provides the hidden steering hook Dyna injects into
// Muse and Claude Code workers it launches. It is an internal transport, not a
// user-installed orchestrator hook.
package harnesshook

import (
	"github.com/spf13/cobra"

	"dyna-agent/internal/harness"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "harness-hook",
		Short:  "Internal harness hooks",
		Hidden: true,
		// TODO: legacy/shim — remove once no worker runtime home still calls
		// "dyna hook steer"; running workers carry harness configs that do.
		Aliases: []string{"hook"},
		Args:    cobra.NoArgs,
		RunE:    func(c *cobra.Command, _ []string) error { return c.Help() },
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
