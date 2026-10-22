package room

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/room"
)

func pinCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pin <room> <post-id>",
		Short: "Pin a post so the room owner keeps it in view",
		Long: "Pin one post. The Dyna desktop app lists pinned posts in a panel next to the feed, newest pin first. " +
			"Pin a post the owner should keep, such as a decision, a result or a card to come back to. " +
			"Use it sparingly: a room holds at most 50 pins. Pinning a pinned post changes nothing. " +
			"The pin records your address. Find post IDs with `dyna room log <room> --all`.",
		Example: "  dyna room pin myroom msg_1a2b3c4d5e6f",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			if err := changePin(cmd.Context(), "Pin", rec.ID, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pinned %s in %s\n", args[1], rec.Name)
			return nil
		},
	}
}

func unpinCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unpin <room> <post-id>",
		Short: "Remove a pin",
		Long:  "Remove a post from the room's pins. A post that is not pinned is not an error.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			if err := changePin(cmd.Context(), "Unpin", rec.ID, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unpinned %s in %s\n", args[1], rec.Name)
			return nil
		},
	}
}

// changePin asks the engine to pin or unpin a post (requirement 2). The
// engine records the caller as the pinner. A change the engine did not make
// exits non-zero with the cause (requirement 26).
func changePin(ctx context.Context, call, roomID, msgID string) error {
	if err := engineClient().Call(ctx, call, []any{roomID, msgID}, nil); err != nil {
		return fmt.Errorf("the pin was not changed: %w", err)
	}
	return nil
}

type pinRow struct {
	room.Pin
	Post room.Message `json:"post"`
}

func pinsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "pins <room> [--json]",
		Short: "List a room's pinned posts, newest pin first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := store().Resolve(args[0])
			if err != nil {
				return err
			}
			pins, err := store().Pins(rec.ID)
			if err != nil {
				return err
			}
			msgs, err := store().Messages(rec.ID)
			if err != nil {
				return err
			}
			byID := make(map[string]room.Message, len(msgs))
			for _, m := range msgs {
				byID[m.ID] = m
			}
			rows := []pinRow{}
			for _, p := range pins {
				if m, ok := byID[p.ID]; ok {
					rows = append(rows, pinRow{Pin: p, Post: m})
				}
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), rows)
			}
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s%s\n", r.ID, time.UnixMilli(r.Post.TS).Format("2006-01-02 15:04:05"), r.Post.From, firstLine(r.Post.Text), attachNote(r.Post))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print each pin with its post as JSON")
	return cmd
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// attachNote is "  [card]" for an HTML card, "  [N files]" for other files.
func attachNote(m room.Message) string {
	files := 0
	for _, a := range m.Attachments {
		if a.Rich() {
			return "  [card]"
		}
		files++
	}
	switch files {
	case 0:
		return ""
	case 1:
		return "  [1 file]"
	}
	return fmt.Sprintf("  [%d files]", files)
}
