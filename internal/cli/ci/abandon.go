package ci

import (
	"fmt"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/localci"
	"github.com/spf13/cobra"
)

// abandonCommand tears down one stale local CI run through the engine, so an
// owner or root never edits the machine by hand.
func abandonCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "abandon <run-id>",
		Short: "Tear down a stale local CI run and release its node reservations",
		Long: "Tear down one local CI run that no engine drives any more, such as a run\n" +
			"left after an engine restart. The run ID is its run folder name, for example\n" +
			"e8cb25882c1b-1793312721843629186. The engine removes the run's folder on the\n" +
			"node and releases its queue reservations. A run whose process still runs is\n" +
			"refused: cancel its trial or pull request instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var out localci.AbandonedRun
			if err := engineclient.New("").Call(c.Context(), "AbandonLocalCIRun", []any{args[0]}, &out); err != nil {
				return err
			}
			if out.Node == "" {
				fmt.Fprintf(c.OutOrStdout(), "No kept folder record existed for run %s. Released %d reservations.\n", out.RunID, out.Reservations)
				return nil
			}
			fmt.Fprintf(c.OutOrStdout(), "Tore down run %s on %s and released %d reservations.\n", out.RunID, out.Node, out.Reservations)
			return nil
		},
	}
}
