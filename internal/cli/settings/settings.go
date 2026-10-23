package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/engineclient"
)

// engineClient keeps engine failure tests on a private socket with no restart wait.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

type listItem struct {
	Key           string `json:"key"`
	Value         any    `json:"value"`
	Display       string `json:"display,omitempty"`
	Source        string `json:"source"`
	LiveVsRestart string `json:"liveVsRestart"`
}

type listing struct {
	V     int        `json:"v"`
	Items []listItem `json:"items"`
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show and change application settings",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(listCommand(), getCommand(), setCommand())
	return cmd
}

func listCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List V1 settings",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			items, err := collectItems()
			if err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), listing{V: 1, Items: items})
			}
			for _, item := range items {
				fmt.Fprintf(c.OutOrStdout(), "%-32s %-12v  %s  %s\n", item.Key, displayValue(item), item.Source, item.LiveVsRestart)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable settings")
	return cmd
}

func getCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Print one settings key",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			items, err := collectItems()
			if err != nil {
				return err
			}
			key := strings.TrimSpace(args[0])
			for _, item := range items {
				if item.Key == key {
					if asJSON {
						return jsonout.Write(c.OutOrStdout(), item)
					}
					fmt.Fprintln(c.OutOrStdout(), displayValue(item))
					return nil
				}
			}
			return fmt.Errorf("unknown settings key %q", key)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable item")
	return cmd
}

func setCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a V1 settings key",
		Long: `Set a V1 settings key.

content.maxCardBytes and content.maxFileBytes set independent size limits
for HTML/JavaScript cards and file attachments (default 25 MiB each).
Accepts 25MiB, "25 MiB", 25MB, 25m or 26214400. MB means MiB (binary units).
Changes apply to the next upload or post without a restart.
A post accepts at most eight attachments and one card. Its total is derived as
7 * maxFileBytes + max(maxFileBytes, maxCardBytes). Already stored posts remain
readable after limits shrink.`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			var reply json.RawMessage
			if err := engineClient().Call(c.Context(), "SetSetting", []any{args[0], args[1]}, &reply); err != nil {
				return err
			}
			// Item.Value is untyped; keep int64 setting values exact in JSON output.
			decoder := json.NewDecoder(bytes.NewReader(reply))
			decoder.UseNumber()
			var it configsvc.Item
			if err := decoder.Decode(&it); err != nil {
				return err
			}
			item := listItem{Key: it.Key, Value: it.Value, Display: it.Display, Source: it.Source, LiveVsRestart: it.LiveVsRestart}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), item)
			}
			fmt.Fprintf(c.OutOrStdout(), "set %s = %v\n", item.Key, displayValue(item))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable item")
	return cmd
}

// collectItems reads the keys through configsvc, which the desktop shares,
// and keeps this command's JSON shape.
func collectItems() ([]listItem, error) {
	all, err := configsvc.Items()
	if err != nil {
		return nil, err
	}
	items := make([]listItem, len(all))
	for i, it := range all {
		items[i] = listItem{Key: it.Key, Value: it.Value, Display: it.Display, Source: it.Source, LiveVsRestart: it.LiveVsRestart}
	}
	return items, nil
}

func displayValue(item listItem) any {
	if item.Display != "" {
		return item.Display
	}
	return item.Value
}
