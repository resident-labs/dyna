package room

import (
	"fmt"
	"time"

	"dyna-agent/internal/cli/goal"
	"github.com/spf13/cobra"
)

func senderMuteCmd() *cobra.Command {
	var duration time.Duration
	var roomRef string
	cmd := &cobra.Command{
		Use:   "sender-mute <session|room> <sender>",
		Short: "Mute cross-room wakes from a sender",
		Long:  "Mute cross-room wakes from a session or room. Posts remain visible as quiet context. Root and owner posts are exempt. Set --for 0 to remove a mute.",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if duration < 0 || duration%time.Second != 0 {
				return fmt.Errorf("duration must be nonnegative and use whole seconds")
			}
			if err := engineClient().Call(c.Context(), "SetSenderMute", []any{goal.MutationRoom(roomRef), args[0], args[1], int64(duration / time.Second)}, nil); err != nil {
				return err
			}
			if duration == 0 {
				_, err := fmt.Fprintln(c.OutOrStdout(), "Mute removed.")
				return err
			}
			_, err := fmt.Fprintf(c.OutOrStdout(), "Sender muted for %s.\n", duration)
			return err
		},
	}
	cmd.Flags().DurationVar(&duration, "for", time.Hour, "mute duration, or 0 to remove")
	cmd.Flags().StringVar(&roomRef, "room", "", "recipient room (default: current room)")
	return cmd
}
