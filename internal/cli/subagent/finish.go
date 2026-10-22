package subagent

import (
	"errors"
	"fmt"
	"os"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

func finishCommand(newManager func() *subagent.Manager) *cobra.Command {
	return &cobra.Command{
		Use:   "finish [session]",
		Short: "Make a session dormant when it owns no responsibilities",
		Long:  "Finish defaults to your DYNA_SESSION and works during your turn. It refuses while the session owns open PR roles, goals, questions, unfinished children, live shell jobs or live workflow runs, and lists each with the command that ends or moves it. A finished session is dormant: it runs nothing, and any message wakes it. Dyna closes sessions that stay dormant for 7 days.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ref := ""
			if len(args) > 0 {
				ref = args[0]
			} else if caller, ok := mode.CurrentSession(); ok {
				ref = caller.ID
			} else {
				return errors.New("finish requires a session or DYNA_SESSION")
			}
			store := newManager().Store
			info, err := store.ResolveSnapshot(ref)
			if err != nil {
				return err
			}
			// The engine cannot read this process's shell job, so the call names it.
			job := os.Getenv(shells.JobEnv)
			if err := engineClient().Call(c.Context(), "FinishSession", []any{info.RoomID, info.ID, job}, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "finished %s (%s); dormant until a message wakes it\n", info.Address(), info.ID)
			return nil
		},
	}
}
