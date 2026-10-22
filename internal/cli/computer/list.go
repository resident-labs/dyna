package computer

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

func printComputerList(c *cobra.Command, values []computers.Status, all, asJSON bool) error {
	rows := make([]computers.Status, 0, len(values))
	for _, value := range values {
		if all || value.State == computers.StateRunning || value.State == computers.StateStarting {
			rows = append(rows, value)
		}
	}
	if asJSON {
		return printJSON(c, rows)
	}
	out := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "ID\tROOM\tSTATE\tAPPS\tIDLE")
	for _, row := range rows {
		names := []string{}
		for i, app := range row.Apps {
			if i == 3 {
				names = append(names, fmt.Sprintf("+%d", len(row.Apps)-i))
				break
			}
			name := app.AppID
			if name == "" {
				name = app.Title
			}
			name = strings.Join(strings.Fields(name), " ")
			if len([]rune(name)) > 36 {
				name = string([]rune(name)[:35]) + "…"
			}
			if name != "" {
				names = append(names, name)
			}
		}
		apps := strings.Join(names, ", ")
		if apps == "" {
			apps = "-"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", row.SessionID, row.Room, row.State, apps, (time.Duration(row.IdleSeconds) * time.Second).String())
	}
	return out.Flush()
}
