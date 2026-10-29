package git

import (
	"fmt"

	"dyna-agent/internal/pr/watch"

	"github.com/spf13/cobra"
)

// issueWatchCommand reads an upstream GitHub issue for a room. It reads only,
// and the room root hears each new comment, close and reopen.
func issueWatchCommand() *cobra.Command {
	var roomRef string
	cmd := &cobra.Command{
		Use:   "watch <url|number>",
		Short: "Watch an upstream GitHub issue for new comments and state changes",
		Long:  "Read an upstream issue, or an issue of this repository, for new comments and closes or reopens. Nothing is written to GitHub. The room root hears each new fact. A number names an issue of the current repository.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s := watch.New()
			ref, err := s.ResolveIssue(c.Context(), args[0], ".")
			if err != nil {
				return err
			}
			rec, err := s.Notify.Rooms.ResolveContext(roomRef, false)
			if err != nil {
				return err
			}
			if err := s.WatchIssue(c.Context(), rec, ref); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Room %s watches %s#%d.\n", rec.Name, ref.Repo, ref.Number)
			return nil
		},
	}
	cmd.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: your room)")
	return cmd
}

// issueUnwatchCommand stops one room's read of an upstream GitHub issue.
func issueUnwatchCommand() *cobra.Command {
	var roomRef string
	cmd := &cobra.Command{
		Use:   "unwatch <url|number>",
		Short: "Stop watching an upstream GitHub issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s := watch.New()
			ref, err := s.ResolveIssue(c.Context(), args[0], ".")
			if err != nil {
				return err
			}
			rec, err := s.Notify.Rooms.ResolveContext(roomRef, false)
			if err != nil {
				return err
			}
			removed, err := s.UnwatchIssue(c.Context(), rec.ID, ref)
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("room %s does not watch %s#%d", rec.Name, ref.Repo, ref.Number)
			}
			fmt.Fprintf(c.OutOrStdout(), "Room %s no longer watches %s#%d.\n", rec.Name, ref.Repo, ref.Number)
			return nil
		},
	}
	cmd.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: your room)")
	return cmd
}
