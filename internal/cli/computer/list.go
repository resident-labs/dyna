package computer

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/userservices"
	"github.com/spf13/cobra"
)

// restartPendingSince is when the service's deferred restart became pending. A
// test replaces it.
var restartPendingSince = func() (time.Time, bool) { return userservices.RestartPendingAt(userservices.Dir()) }

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
	if err := out.Flush(); err != nil {
		return err
	}
	if at, ok := restartPendingSince(); ok {
		fmt.Fprintf(c.OutOrStdout(), "restart pending since %s: the computer service restarts once no computer has an app open\n", at.Local().Format("2006-01-02 15:04"))
	}
	return nil
}
