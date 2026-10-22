// Package maintenance shows what the engine's maintenance passes removed.
package maintenance

import (
	"fmt"
	"time"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/store"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "maintenance", Short: "Show what maintenance removed", Args: cobra.NoArgs}
	cmd.AddCommand(statusCommand(store.DataDir))
	return cmd
}

func statusCommand(dataDir func() string) *cobra.Command {
	var last int
	var asJSON bool
	cmd := &cobra.Command{Use: "status", Short: "List recent maintenance passes and what each freed", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		reports, err := maintenance.RecentReports(dataDir(), last)
		if err != nil {
			return err
		}
		if asJSON {
			return jsonout.Write(c.OutOrStdout(), reports)
		}
		if len(reports) == 0 {
			_, err := fmt.Fprintln(c.OutOrStdout(), "No maintenance passes recorded yet.")
			return err
		}
		for _, r := range reports {
			line := fmt.Sprintf("%s  %s  %s", r.At.Local().Format("2006-01-02 15:04"), r.Duration.Round(time.Millisecond), r.Summary)
			if r.Error != "" {
				line += "  error: " + r.Error
			}
			if _, err := fmt.Fprintln(c.OutOrStdout(), line); err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().IntVarP(&last, "last", "n", 10, "number of passes to show")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
