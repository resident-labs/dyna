package subagent

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/subagent"
)

func consolidationCommand(newManager func() *subagent.Manager) *cobra.Command {
	var opts subagent.ConsolidationOptions
	var asJSON bool
	cmd := &cobra.Command{Use: "consolidate <session> <session>... --keep <session>", Short: "Consolidate selected same-room tasks into one kept agent", Long: `Select 2 to 16 open sessions from one room, including the agent to keep.
All selected agents get one shared maximum 30-second handoff window. Failed
agents use transcript context immediately. The kept agent starts a
fresh model conversation with every task, history and worktree reference.
Profile/model default to the kept agent and stay fixed during recovery.

Sources stop before replacement startup, then become readable archives only
after startup proves success. Files, branches and git worktrees stay intact.
Children, inboxes and active PR roles move to the kept agent.
Conflicting author/reviewer roles need a new
independent reviewer. Completed verdicts and old transcripts stay readable.

Repeating a request recovers the same durable operation. Use status, resume
and cancel with its operation ID. A failed startup remains reserved until
resume or cancel; it does not restart sources or silently lose pending work.
Cancel works before startup or after a startup failure.`, Args: cobra.MinimumNArgs(2), RunE: func(c *cobra.Command, args []string) error {
		opts.Sessions = args
		info, err := newManager().Store.ResolveSnapshot(args[0])
		if err != nil {
			return err
		}
		// The engine checks every selected session against the caller.
		var state subagent.ConsolidationState
		if err := engineClient().Call(c.Context(), "ConsolidateSessions", []any{info.RoomID, opts}, &state); err != nil {
			return err
		}
		if asJSON {
			return jsonout.Write(c.OutOrStdout(), state)
		}
		fmt.Fprintf(c.OutOrStdout(), "Consolidation %s: %s; kept session %s.\n", state.ID, state.Phase, state.Target)
		return nil
	}}
	cmd.Flags().StringVar(&opts.Target, "keep", "", "selected session to keep (required)")
	_ = cmd.MarkFlagRequired("keep")
	cmd.Flags().StringVar(&opts.Profile, "profile", "", "target profile (default: kept agent)")
	cmd.Flags().StringVar(&opts.Model, "model", "", "target model (default: kept agent)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print saved operation state")
	var roomRef string
	for _, action := range []string{"status", "resume", "cancel"} {
		action := action
		sub := &cobra.Command{Use: action + " <operation-id>", Short: action + " a saved consolidation", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			mgr := newManager()
			rec, err := mgr.RoomStore().ResolveContext(roomRef, false)
			if err != nil {
				return err
			}
			states, err := mgr.SessionConsolidations(rec.ID)
			if err != nil {
				return err
			}
			var state subagent.ConsolidationState
			for _, s := range states {
				if s.ID == args[0] {
					state = s
				}
			}
			if state.ID == "" {
				return fmt.Errorf("no such consolidation in room %s", rec.Name)
			}
			if action == "status" {
				return jsonout.Write(c.OutOrStdout(), state)
			}
			// Resume and cancel change every selected session; the engine
			// checks each one against the caller.
			method := map[string]string{"resume": "ResumeSessionConsolidation", "cancel": "CancelSessionConsolidation"}[action]
			if err := engineClient().Call(c.Context(), method, []any{rec.ID, args[0]}, &state); err != nil {
				return err
			}
			return jsonout.Write(c.OutOrStdout(), state)
		}}
		sub.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: the stamped room, else the current repo's room)")
		sub.Flags().Bool("json", true, "print saved operation state")
		cmd.AddCommand(sub)
	}
	return cmd
}
func consolidationRunnerCommand(newManager func() *subagent.Manager) *cobra.Command {
	return &cobra.Command{Use: "_consolidate <target> <operation-id>", Hidden: true, Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, args []string) error {
		return newManager().ProcessConsolidation(context.Background(), args[0], args[1])
	}}
}
