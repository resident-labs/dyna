package room

import (
	"fmt"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

func sessionControlsCmd(action string) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{Use: action + " <room>", Short: map[string]string{"pause": "Hold new session turns and delivery; active calls finish", "resume": "Resume held session work without changing native identity", "clear-dormant": "Close every dormant session that still owns nothing", "session-controls": "Show room session pause state and counts"}[action], Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		m := notify.Default().Sessions
		if action == "session-controls" {
			state, err := m.RoomSessionControls(args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(c.OutOrStdout(), state)
			}
			fmt.Fprintf(c.OutOrStdout(), "paused=%t running=%d held=%d dormant=%d\n", state.Paused, state.Running, state.Held, state.Dormant)
			return nil
		}
		// The engine applies the change and checks the caller (requirement 2).
		var result subagent.RoomSessionMutation
		method := map[string]string{"pause": "PauseRoomSessions", "resume": "ResumeRoomSessions", "clear-dormant": "ClearDormantSessions"}[action]
		if err := engineClient().Call(c.Context(), method, []any{args[0]}, &result); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(c.OutOrStdout(), result)
		}
		for _, row := range result.Results {
			name := row.Name
			if name == "" {
				name = row.Session
			}
			fmt.Fprintf(c.OutOrStdout(), "%s: %s", name, row.Status)
			if row.Error != "" {
				fmt.Fprintf(c.OutOrStdout(), " (%s)", row.Error)
			}
			fmt.Fprintln(c.OutOrStdout())
		}
		return nil
	}}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print ordered per-session results and room state as JSON")
	return cmd
}
