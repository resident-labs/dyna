package subagent

import (
	"dyna-agent/internal/subagent"
	"fmt"
	"github.com/spf13/cobra"
)

func reopenCommand(newManager func() *subagent.Manager) *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <address|session-id>",
		Short: "Reopen a closed resumable session with its existing history",
		Long:  "Reopen the same conversation without starting a turn. An address selects its most recently closed session. Use a session ID to select exact history. The session name must be free in its room.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			info, err := newManager().Store.ResolveClosed(args[0])
			if err != nil {
				return err
			}
			if err := engineClient().Call(c.Context(), "ReopenSession", []any{info.RoomID, info.ID}, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "reopened %s (%s)\n", info.Address(), info.ID)
			return nil
		},
	}
}
