package goal

import (
	"dyna-agent/internal/goals"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func waitCommand(o Options, roomRef *string) *cobra.Command {
	var until string
	cmd := &cobra.Command{
		Use:   "wait <id> --until <duration|RFC3339>",
		Short: "Record a wait deadline without marking goal progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			deadline, err := parseUntil(until, time.Now())
			if err != nil {
				return err
			}
			var g goals.Goal
			err = o.Client.Call(c.Context(), "WaitGoal", []any{MutationRoom(*roomRef), args[0], time.UnixMilli(deadline)}, &g)
			if err != nil {
				return err
			}
			return printGoal(c, g)
		},
	}
	cmd.Flags().StringVar(&until, "until", "", "wait deadline: a positive duration or future RFC3339 time")
	return cmd
}

func parseUntil(value string, now time.Time) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("--until is required")
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration > 0 && now.Add(duration).UnixMilli() > now.UnixMilli() {
			return now.Add(duration).UnixMilli(), nil
		}
	} else if deadline, err := time.Parse(time.RFC3339, value); err == nil && deadline.UnixMilli() > now.UnixMilli() {
		return deadline.UnixMilli(), nil
	}
	return 0, fmt.Errorf("invalid --until %q: use a positive duration or future RFC3339 time", value)
}
