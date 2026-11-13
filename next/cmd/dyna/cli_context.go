package main

import (
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func contextCommand(getClient func() client) *cobra.Command {
	contextCmd := &cobra.Command{Use: "context"}
	contextCmd.AddCommand(&cobra.Command{Use: "show SECTION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().ContextSnapshot(cmd.Context(), contract.ContextSnapshotRequest{Section: contract.ContextSectionKey(args[0])})
		if err != nil {
			return err
		}
		for _, section := range result.Sections {
			if _, err = fmt.Fprintln(cmd.OutOrStdout(), section.Text); err != nil {
				return err
			}
		}
		return nil
	}})
	return contextCmd
}
