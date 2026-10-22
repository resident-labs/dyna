package subagent

import (
	"fmt"

	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

func dismissPendingCommand(newManager func() *subagent.Manager) *cobra.Command {
	var confirmed bool
	cmd := &cobra.Command{
		Use:   "dismiss-pending <session> <message>",
		Short: "Owner: dismiss an idle session's pending post from any sender",
		Long:  "Read the post first with dyna room read <room> <message>. Then use --confirm to prevent session delivery. History stays available. Reading alone does not acknowledge delivery. Active delivery cannot be recalled.",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			info, err := newManager().Store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			if err := engineClient().Call(c.Context(), "DismissPendingPost", []any{info.ID, args[1], confirmed}, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "dismissed %s for %s; history preserved\n", args[1], args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&confirmed, "confirm", false, "confirm that this session must not receive this post")
	return cmd
}
