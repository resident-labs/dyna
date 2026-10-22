package pr

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"dyna-agent/internal/engineclient"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"

	"github.com/spf13/cobra"
)

// healthCommand reads the room's CI health: the newest runs against the
// budget, then the tests that ran near the limit. The text form is what a
// person reads; --json is the same data for a script.
func healthCommand(o Options, roomFor func(*watch.Service, string) (string, error)) *cobra.Command {
	var runs int
	health := &cobra.Command{Use: "health", Short: "Show the room's CI runs and its slow tests", Args: cobra.NoArgs}
	roomFlag := health.Flags().String("room", "", "room name or ID (default: current stamped room)")
	health.Flags().IntVar(&runs, "runs", 12, "recent runs to show")
	jsonFlag := health.Flags().Bool("json", false, "print the full data as JSON")
	health.RunE = func(c *cobra.Command, _ []string) error {
		roomID, err := roomFor(o.Service(), *roomFlag)
		if err != nil {
			return err
		}
		var health prview.CIHealth
		if err := engineclient.New("").Call(c.Context(), "CIHealth", []any{roomID, runs}, &health); err != nil {
			return err
		}
		if *jsonFlag {
			enc := json.NewEncoder(c.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(health)
		}
		_, err = io.WriteString(c.OutOrStdout(), formatHealth(health))
		return err
	}
	return health
}

// formatHealth writes the text report: the budget, the newest runs, then the
// tests over the limit and the ones near it.
func formatHealth(h prview.CIHealth) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CI health · %s · test limit %s · warn %s · fail %s\n",
		h.Room, ms(h.LimitMs), ms(h.WarnMs), ms(h.FailMs))
	if len(h.Runs) == 0 {
		b.WriteString("No CI runs recorded yet. A run is recorded when the watcher inspects it.\n")
	}
	var over []prview.CIHealthTest
	for _, test := range h.Tests {
		if test.Breaches > 0 {
			over = append(over, test)
		}
	}
	fmt.Fprintf(&b, "runs %d · tests tracked %d · over the limit %d\n", len(h.Runs), len(h.Tests), len(over))
	for _, run := range h.Runs {
		fmt.Fprintf(&b, "  %s %s · %s busy", runLabel(run), runState(run), ms(run.BusyMs))
		if run.QueueMs > 0 {
			fmt.Fprintf(&b, ", %s waiting", ms(run.QueueMs))
		}
		fmt.Fprintf(&b, ", budget %s\n", budgetWord(run.BusyMs, run.WarnMs, run.FailMs))
	}
	if len(h.Tests) == 0 {
		return b.String()
	}
	b.WriteString("tests near or over the limit:\n")
	for _, test := range h.Tests {
		fmt.Fprintf(&b, "  %s · %s · %s · last %s, typical %s · %d breaches · %s\n",
			test.Name, test.Package, features(test.Features), ms(test.LastMs), ms(test.TypicalMs), test.Breaches, test.State)
	}
	return b.String()
}

func runLabel(run prview.CIHealthRun) string {
	if run.PR > 0 {
		return fmt.Sprintf("#%d", run.PR)
	}
	return run.Token
}

func runState(run prview.CIHealthRun) string {
	switch {
	case run.Conclusion != "":
		return run.Conclusion
	case run.Status != "":
		return run.Status
	default:
		return "unknown"
	}
}

// budgetWord names where the run sits against its own budget lines.
func budgetWord(busy, warn, fail int64) string {
	switch {
	case fail > 0 && busy >= fail:
		return "over fail"
	case warn > 0 && busy >= warn:
		return "over warn"
	default:
		return "within"
	}
}

func features(ids []string) string {
	if len(ids) == 0 {
		return "unmapped"
	}
	return strings.Join(ids, ", ")
}

func ms(value int64) string {
	d := time.Duration(value) * time.Millisecond
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
