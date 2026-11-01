package main

import (
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/migrate"
	"github.com/spf13/cobra"
)

// LEGACY(v1-transfer): this whole file, the dyna import command. Remove after the transfer.
func importCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{Use: "import BUNDLE", Short: "Check a v1 migration bundle", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dryRun {
			return fmt.Errorf("only --dry-run is available")
		}
		report, err := migrate.DryRun(args[0])
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and count without writing a store")
	return cmd
}
