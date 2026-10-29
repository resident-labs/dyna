// Package state provides supported access to Dyna's persistent state.
package state

import (
	"fmt"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
	"github.com/spf13/cobra"
)

// NewCommand builds the dyna state command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Inspect and copy Dyna state",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(snapshotCommand(store.DataDir))
	return cmd
}

func snapshotCommand(dataDir func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot <path>",
		Short: "Write a consistent, private SQLite state snapshot",
		Long: "Write a consistent snapshot of Dyna's SQLite state with VACUUM INTO.\n" +
			"The command does not start the engine or migrate the database. Concurrent\n" +
			"writers can continue while SQLite reads one consistent snapshot.\n\n" +
			"The destination must not exist. Dyna never overwrites it or follows a\n" +
			"destination symlink. The completed file has mode 0600. It contains private\n" +
			"Dyna state, so keep it in private scratch storage and remove it when finished.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			dir := dataDir()
			db, err := database.Open(dir)
			if err != nil {
				return fmt.Errorf("open Dyna state: %w", err)
			}
			defer database.Close(dir)
			path, err := database.Snapshot(c.Context(), db, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(c.OutOrStdout(), path)
			return err
		},
	}
}
