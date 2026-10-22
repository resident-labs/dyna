package computer

import (
	"fmt"

	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

func newControlCommand(ensure func(*cobra.Command) (computers.Meta, error)) *cobra.Command {
	var reason string
	control := &cobra.Command{Use: "control human --reason <text>", Short: "Ask the person to take control of your computer", Example: `dyna computer control human --reason "Please sign in"`, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if args[0] != "human" {
			return fmt.Errorf("control takes one mode: human")
		}
		var err error
		if reason, err = computers.RequiredReason(reason); err != nil {
			return err
		}
		m, err := ensure(c)
		if err != nil {
			return err
		}
		if _, err := computers.NewClient(m.SessionID).RequestHuman(c.Context(), reason); err != nil {
			return err
		}
		fmt.Fprintln(c.ErrOrStderr(), "Agent input waits until the person returns control.")
		return nil
	}}
	control.Flags().StringVar(&reason, "reason", "", "required: what the person should do (maximum 80 characters)")
	return control
}
