package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func visualCommand() *cobra.Command {
	var allowNet bool
	cmd := &cobra.Command{Use: "visual"}
	check := &cobra.Command{Use: "check FILE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		data, err := visual.ReadFile(args[0])
		if err != nil {
			return err
		}
		if _, _, err = visual.Meta(args[0], data); err != nil {
			return err
		}
		result, err := visual.Inspect(cmd.Context(), data, visual.CSP(allowNet), allowNet)
		if err != nil {
			return err
		}
		dir, err := os.MkdirTemp(os.Getenv("DYNA_SCRATCH"), "visual-")
		if err != nil {
			return err
		}
		write := func(name string, data []byte) error {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		}
		printVisualChecks(cmd.OutOrStdout(), result.Checks)
		if err := write("banner.webp", result.Banner); err != nil {
			return err
		}
		if err := write("thumbnail.webp", result.Thumb); err != nil {
			return err
		}
		for _, shot := range result.Widths {
			if err := write(fmt.Sprintf("%d.webp", shot.Width), shot.Image); err != nil {
				return err
			}
		}
		return nil
	}}
	check.Flags().BoolVar(&allowNet, "allow-net", false, "Allow HTTPS resources")
	cmd.AddCommand(check)
	return cmd
}

func printVisualChecks(w io.Writer, checks []contract.VisualCheck) {
	for _, check := range checks {
		fmt.Fprintf(w, "%s: %s %s\n", check.Name, check.Status, check.Detail)
	}
}
