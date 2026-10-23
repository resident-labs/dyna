package subagent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/transcript"
)

func transferCommand(newManager func() *subagent.Manager) *cobra.Command {
	var opts subagent.TransferOptions
	var asJSON bool
	cmd := &cobra.Command{Use: "transfer <session> --profile <profile> [--model <model>]", Short: "Transfer to a fresh model conversation while preserving the Dyna session", Long: `Ask the current model for a written handoff, then start a fresh native conversation
with the chosen profile and optional model. The Dyna session ID, address,
hierarchy, children, files, history and tracked PR ownership stay the same.
The new model receives a full charter, the handoff or transcript fallback,
worktree and branch hints, and exact commands to read the old turns.

The handoff wait is bounded (default 30 seconds). A failed old agent uses the
transcript immediately. Status and cancel are durable; repeating the same
request while it runs recovers a dead coordinator without creating another
replacement. Naming the profile and model the session already runs starts
another conversation in the same session instead.
Use --retry after a failed or cancelled attempt. Replacement startup has a
60-second output deadline. Startup failure restores the prior profile and
native resume ID; it keeps the failed turn readable. Cancel is available
while waiting, before the old turn stops.`, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if opts.Profile == "" {
			return errors.New("--profile is required; run dyna profiles list")
		}
		info, err := newManager().Store.ResolveSnapshot(args[0])
		if err != nil {
			return err
		}
		var t subagent.TransferState
		if err := engineClient().Call(c.Context(), "TransferSession", []any{info.RoomID, info.ID, opts}, &t); err != nil {
			return err
		}
		if asJSON {
			return jsonout.Write(c.OutOrStdout(), t)
		}
		fmt.Fprintf(c.OutOrStdout(), "transfer %s: %s → %s (%s); inspect with `dyna subagent transfer status %s`\n", t.ID, t.PreviousProfile, t.Profile, t.Phase, t.Session)
		return nil
	}}
	cmd.Flags().StringVar(&opts.Profile, "profile", "", "target profile (required)")
	cmd.Flags().StringVar(&opts.Model, "model", "", "model override (default: the profile's model)")
	cmd.Flags().IntVar(&opts.WaitSeconds, "wait-seconds", 30, "seconds to wait for a written handoff (0 to 120)")
	cmd.Flags().BoolVar(&opts.Retry, "retry", false, "retry a failed or cancelled transfer")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print durable transfer state as JSON")
	statusJSON := false
	status := &cobra.Command{Use: "status <session>", Short: "Show transfer progress and recovery information", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		t, err := newManager().TransferStatus(args[0])
		if err != nil {
			return err
		}
		if statusJSON {
			return jsonout.Write(c.OutOrStdout(), t)
		}
		if t == nil {
			fmt.Fprintln(c.OutOrStdout(), "No transfer.")
			return nil
		}
		fmt.Fprintf(c.OutOrStdout(), "Transfer %s: %s\n%s → %s / %s\nDeadline: %s\nSource: %s\nReason: %s\n", t.ID, t.Phase, t.PreviousProfile, t.Profile, t.Model, t.Deadline.Format(time.RFC3339), t.Source, t.Reason)
		if t.Error != "" {
			fmt.Fprintf(c.OutOrStdout(), "Error: %s\nThe prior profile and native resume ID are available. Fix startup and repeat the transfer command with --retry.\n", t.Error)
		}
		return nil
	}}
	status.Flags().BoolVar(&statusJSON, "json", false, "machine-readable transfer state")
	cancel := &cobra.Command{Use: "cancel <session>", Short: "Cancel a transfer still waiting for a handoff", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		info, err := newManager().Store.ResolveSnapshot(args[0])
		if err != nil {
			return err
		}
		var t subagent.TransferState
		if err := engineClient().Call(c.Context(), "CancelSessionTransfer", []any{info.RoomID, info.ID}, &t); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "transfer %s: %s\n", t.ID, t.Phase)
		return nil
	}}
	var token, dir, branch string
	handoff := &cobra.Command{Use: "handoff <session> <text-or-@file> --token <id>", Short: "Submit the requested handoff with actual task worktree and branch hints", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, args []string) error {
		if token == "" {
			return errors.New("--token is required")
		}
		text, err := textArg(args[1])
		if err != nil {
			return err
		}
		info, err := newManager().Store.ResolveSnapshot(args[0])
		if err != nil {
			return err
		}
		if err := engineClient().Call(c.Context(), "SubmitTransferHandoff", []any{info.RoomID, info.ID, token, text, dir, branch}, nil); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Handoff saved. The transfer continues in the background.")
		return nil
	}}
	handoff.Flags().StringVar(&token, "token", "", "current transfer ID from the request")
	handoff.Flags().StringVar(&dir, "dir", "", "actual task worktree, which may differ from the launch directory")
	handoff.Flags().StringVar(&branch, "branch", "", "task branch hint")
	cmd.AddCommand(status, cancel, handoff)
	return cmd
}

// history gives the replacement a precise search/slice surface for old turns.
func historyCommand(newManager func() *subagent.Manager) *cobra.Command {
	var from, to, tail int
	var search string
	cmd := &cobra.Command{Use: "history <session> [--from-turn N] [--to-turn N] [--search text] [--tail N]", Short: "Read or search a range of old turns without losing the session history", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if from < 1 || to < 0 || tail < 0 || to > 0 && to < from {
			return errors.New("invalid turn range or tail count")
		}
		loader := transcript.NewLoader(newManager().Store)
		info, err := newManager().Store.ResolveSnapshot(args[0])
		if err != nil {
			return err
		}
		end := info.Turns
		if to > 0 && to < end {
			end = to
		}
		var lines []string
		for n := from; n <= end; n++ {
			session, err := loader.LoadTurn(info.ID, n)
			if err != nil {
				return err
			}
			for _, t := range session.Turns {
				var text strings.Builder
				transcript.WriteTurn(&text, t, transcript.PlainOptions{Agent: info.Profile, Expand: true})
				for _, line := range strings.Split(strings.TrimSuffix(text.String(), "\n"), "\n") {
					if search == "" || strings.Contains(strings.ToLower(line), strings.ToLower(search)) {
						if tail == 0 {
							fmt.Fprintf(c.OutOrStdout(), "turn %d: %s\n", n, line)
							continue
						}
						lines = append(lines, fmt.Sprintf("turn %d: %s", n, line))
						if tail > 0 && len(lines) > tail {
							lines = lines[1:]
						}
					}
				}
			}
		}
		for _, line := range lines {
			fmt.Fprintln(c.OutOrStdout(), line)
		}
		return nil
	}}
	cmd.Flags().IntVar(&from, "from-turn", 1, "first turn to read")
	cmd.Flags().IntVar(&to, "to-turn", 0, "last turn to read (default: latest)")
	cmd.Flags().IntVar(&tail, "tail", 0, "keep only the last N matching lines")
	cmd.Flags().StringVar(&search, "search", "", "case-insensitive text to find")
	return cmd
}
