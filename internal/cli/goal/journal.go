package goal

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

func journalCommand(o Options, roomRef *string) *cobra.Command {
	var tail int
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Show the keeper's check-in journal, oldest of the last entries first",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			s, r, _, err := Caller(o.Service(), *roomRef)
			if err != nil {
				return err
			}
			entries, _, err := s.Store.Journal(r.ID, "", tail)
			if err != nil {
				return err
			}
			slices.Reverse(entries)
			if asJSON, _ := c.Flags().GetBool("json"); asJSON {
				return json.NewEncoder(c.OutOrStdout()).Encode(entries)
			}
			if len(entries) == 0 {
				_, err = fmt.Fprintln(c.OutOrStdout(), "No journal entries yet.")
				return err
			}
			for _, e := range entries {
				flag := ""
				if e.NeedsAction {
					flag = " · needs action"
				}
				fmt.Fprintf(c.OutOrStdout(), "%s %s%s\n", e.ID, e.At.Local().Format("2006-01-02 15:04"), flag)
				for _, l := range e.Entries {
					fmt.Fprintf(c.OutOrStdout(), "  %s · %s · %s (%s)\n", l.Agent, l.Status, l.Summary, strings.Join(l.Goals, ", "))
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 10, "number of newest entries to show")
	return cmd
}
