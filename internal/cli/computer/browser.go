package computer

import (
	"fmt"

	"dyna-agent/internal/computers"
	roompkg "dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

func resolveActor() (id, roomID string, err error) {
	info, err := subagent.Actor(roompkg.DefaultStore(), subagent.DefaultStore().Resolve, "")
	if err != nil {
		return "", "", err
	}
	return computers.ActorID(info.ID, info.RoomID), info.RoomID, nil
}

// NewBrowserCommand connects web tools to the computer's shared headed browser.
func NewBrowserCommand() *cobra.Command {
	root := &cobra.Command{Use: "browser", Short: "Start or reuse your computer's headed Chromium", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		id, roomID, err := resolveActor()
		if err != nil {
			return err
		}
		if _, err := computers.Ensure(c.Context(), id, roomID); err != nil {
			return err
		}
		var browser computers.Browser
		if err := computers.NewClient(id).Call(c.Context(), computers.Request{Op: "browser.start"}, &browser); err != nil {
			return err
		}
		module, err := playwrightModule(c.Context())
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), browser.Endpoint)
		fmt.Fprintf(c.OutOrStdout(), "const { chromium } = await import(%q);\n", module)
		fmt.Fprintf(c.OutOrStdout(), "const browser = await chromium.connectOverCDP(%q); const context = browser.contexts()[0]; const page = context.pages().at(-1);\nconst cdp = await context.newCDPSession(page);\n", browser.Endpoint)
		return nil
	}}
	root.AddCommand(&cobra.Command{Use: "stop", Short: "Stop your computer's Chromium", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		id, _, err := resolveActor()
		if err != nil {
			return err
		}
		var result map[string]bool
		if err := computers.NewClient(id).Call(c.Context(), computers.Request{Op: "browser.stop"}, &result); err != nil {
			return err
		}
		return printJSON(c, result)
	}})
	root.AddCommand(NewBrowserOpenCommand())
	root.AddCommand(NewBrowserTraceCommand())
	root.AddCommand(NewBrowserPresetCommand())
	root.AddCommand(newRecordCommand(ensureComputer))
	return root
}
