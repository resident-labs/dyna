package sync

import (
	"fmt"
	"os"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/room"
	"github.com/spf13/cobra"
)

// OperationCommand starts the same saved operation used by the UI.
func OperationCommand(name, method, short string) *cobra.Command {
	var repo, roomRef string
	var wait bool
	cmd := &cobra.Command{Use: name, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if repo == "" {
			var err error
			repo, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		rm, err := room.DefaultStore().ResolveContext(roomRef, false)
		if err != nil {
			return err
		}
		var result struct {
			Started bool `json:"started"`
		}
		if err := engineclient.New("").Call(c.Context(), method, []any{rm.ID, repo}, &result); err != nil {
			return err
		}
		if !wait {
			return jsonout.Write(c.OutOrStdout(), result)
		}
		progress, err := lanesync.New().Wait(c.Context(), repo)
		if progress.Head != "" {
			fmt.Fprintln(c.OutOrStdout(), progress.Head)
		}
		if progress.PRURL != "" {
			fmt.Fprintln(c.OutOrStdout(), progress.PRURL)
		}
		return err
	}}
	cmd.Flags().StringVar(&repo, "repo", "", "repository path (default: current directory)")
	cmd.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: current room)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the saved operation to finish")
	return cmd
}
