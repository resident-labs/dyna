package ci

import (
	"fmt"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/watch"
	"github.com/spf13/cobra"
)

func registryCommands() []*cobra.Command {
	s := localci.Store{}
	return []*cobra.Command{modeCommand(s, func() (string, error) {
		rec, err := watch.New().Notify.Rooms.ResolveContext("", true)
		return rec.ID, err
	})}
}

func modeCommand(s localci.Store, roomID func() (string, error)) *cobra.Command {
	return &cobra.Command{
		Use: "mode [github|local|both]", Short: "Show or set CI mode for the current room", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 0 {
				if err := localci.ValidateMode(args[0]); err != nil {
					return err
				}
			}
			id, err := roomID()
			if err != nil {
				return err
			}
			if len(args) != 0 {
				if err := s.SetMode(c.Context(), id, args[0]); err != nil {
					return err
				}
			}
			mode, err := s.Mode(c.Context(), id)
			if err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), mode)
			return nil
		},
	}
}
