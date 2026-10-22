package computer

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"syscall"

	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

//go:embed browser_preset.mjs
var presetScript []byte

// presetViewport is a viewport in the shape of Playwright's browser.newContext options.
type presetViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// PresetOptions is a viewport preset in the shape of Playwright's browser.newContext options.
type PresetOptions struct {
	Viewport          presetViewport `json:"viewport"`
	DeviceScaleFactor float64        `json:"deviceScaleFactor"`
	IsMobile          bool           `json:"isMobile"`
	HasTouch          bool           `json:"hasTouch"`
	UserAgent         string         `json:"userAgent,omitempty"`
}

var presets = map[string]PresetOptions{
	"desktop": {Viewport: presetViewport{1280, 800}, DeviceScaleFactor: 1},
	"phone": {Viewport: presetViewport{390, 844}, DeviceScaleFactor: 3, IsMobile: true, HasTouch: true,
		UserAgent: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36"},
}

// NewBrowserPresetCommand makes pages behave like a desktop or a phone, not only look like one.
func NewBrowserPresetCommand() *cobra.Command {
	var options bool
	command := &cobra.Command{Use: "preset desktop|phone|off", Short: "Emulate a desktop (1280x800) or phone (390x844, 3x, touch, mobile user agent) viewport", Long: `Emulate a desktop or phone viewport on every tab of the shared browser, including tabs opened later.
The emulation lasts until 'preset off' or the browser stops. A new Playwright connection to a page resets that
page's emulation, so scripts that drive the page with Playwright should create their own context instead:
'preset phone --options' prints the matching browser.newContext options as JSON and changes nothing.`, Args: cobra.ExactArgs(1), ValidArgs: []string{"desktop", "phone", "off"}, RunE: func(c *cobra.Command, args []string) error {
		preset, known := presets[args[0]]
		if !known && args[0] != "off" {
			return fmt.Errorf("unknown preset %q: use desktop, phone or off", args[0])
		}
		if options {
			if !known {
				return fmt.Errorf("--options needs desktop or phone")
			}
			return printJSON(c, preset)
		}
		id, roomID, err := resolveActor()
		if err != nil {
			return err
		}
		if _, err := computers.Ensure(c.Context(), id, roomID); err != nil {
			return err
		}
		if !known {
			if pid := holderPID(holderDir(id, "preset")); pid > 0 {
				if err := killHolder(c.Context(), pid); err != nil && err != syscall.ESRCH {
					return err
				}
			}
			fmt.Fprintln(c.OutOrStdout(), "preset off")
			return nil
		}
		b, err := json.Marshal(preset)
		if err != nil {
			return err
		}
		if _, err := startHolder(c.Context(), id, "preset", presetScript, true, string(b)); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "preset %s on; it stays until 'dyna browser preset off' or the browser stops\n", args[0])
		return nil
	}}
	command.Flags().BoolVar(&options, "options", false, "print Playwright browser.newContext options and change nothing")
	return command
}
