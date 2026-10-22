package computer

import (
	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

// NewBrowserOpenCommand opens a visible tab in the computer's shared browser.
func NewBrowserOpenCommand() *cobra.Command {
	var intent string
	command := &cobra.Command{Use: "open URL", Short: "Open a tab in your shared browser (requires --status)", Args: cobra.ExactArgs(1), PreRunE: func(c *cobra.Command, args []string) error {
		var err error
		intent, err = computers.RequiredStatus(intent)
		return err
	}, RunE: func(c *cobra.Command, args []string) error {
		id, roomID, err := resolveActor()
		if err != nil {
			return err
		}
		if _, err := computers.Ensure(c.Context(), id, roomID); err != nil {
			return err
		}
		var reply map[string]bool
		if err := computers.NewClient(id).Call(c.Context(), computers.Request{Op: "browser.navigate", BrowserArgs: args, Status: intent}, &reply); err != nil {
			return err
		}
		return printJSON(c, reply)
	}}
	command.Flags().StringVar(&intent, "status", "", "required: short intent (maximum 80 characters)")
	return command
}
