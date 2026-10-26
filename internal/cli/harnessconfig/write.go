package harnessconfig

import (
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/engineclient"
	harnessmgr "dyna-agent/internal/harness/config"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"github.com/spf13/cobra"
)

// engineClient keeps engine failure tests on a private socket without restart waits.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

// AddWriteCommands adds catalog edits and explicit account config generation.
func AddWriteCommands(parent *cobra.Command, kind string) {
	for _, verb := range []string{"add", "rm", "enable", "disable", "sync"} {
		var harness, command, url, source string
		var args, envKeys []string
		var timeoutSeconds int
		var exposure string
		c := &cobra.Command{Use: verb + " <name>", Short: verb + " Dyna " + kind + " entries", Args: cobra.ExactArgs(1)}
		if verb == "sync" {
			c.Use = "sync"
			c.Short = "Generate private native config for a selected account"
			c.Args = cobra.NoArgs
			c.Flags().String("account", "", "private account ID (required)")
		}
		c.Flags().StringVar(&harness, "harness", "", "select a harness ID")
		if verb == "add" {
			if kind == "mcp" {
				c.Flags().StringVar(&command, "command", "", "MCP executable")
				c.Flags().StringArrayVar(&args, "arg", nil, "MCP argument (repeatable)")
				c.Flags().StringArrayVar(&envKeys, "env-key", nil, "environment key reference (repeatable; never a value)")
				c.Flags().StringVar(&url, "url", "", "HTTP MCP endpoint without credentials")
				c.Flags().IntVar(&timeoutSeconds, "timeout-seconds", 0, "per-server request timeout in seconds (0 keeps the harness default)")
				c.Flags().StringVar(&exposure, "exposure", "", "Pi tool exposure: codemode, deferred, direct or hidden (empty keeps the default)")
			} else {
				c.Flags().StringVar(&source, "source", "", "absolute skill directory containing SKILL.md")
			}
		}
		c.RunE = func(cmd *cobra.Command, names []string) error {
			if kind != "mcp" && kind != "skill" {
				return errors.New("unsupported config kind")
			}
			if verb == "sync" {
				account, _ := cmd.Flags().GetString("account")
				if account == "" {
					return errors.New("sync requires --account with an explicit private account ID")
				}
				if harness == "" {
					return errors.New("sync requires --harness and a private account scope")
				}
				var result harnessruntime.ConfigSyncResult
				if err := engineClient().Call(cmd.Context(), "SyncCatalogAccount", []any{kind, harness, account}, &result); err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			name := names[0]
			var err error
			switch verb {
			case "add":
				enabled := []string{}
				if harness != "" {
					enabled = append(enabled, harness)
				}
				if kind == "mcp" {
					entry := harnessmgr.ManagedMCP{Name: name, MCPDefinition: harnessmgr.MCPDefinition{Command: command, Args: args, EnvKeys: envKeys, URL: url, TimeoutSeconds: timeoutSeconds, Exposure: harnessmgr.MCPExposure(exposure)}, EnabledHarnesses: enabled}
					err = engineClient().Call(cmd.Context(), "AddCatalogMCP", []any{entry, harness}, nil)
				} else {
					entry := harnessmgr.ManagedSkill{Name: name, Source: source, EnabledHarnesses: enabled}
					err = engineClient().Call(cmd.Context(), "AddCatalogSkill", []any{entry, harness}, nil)
				}
			case "rm":
				err = engineClient().Call(cmd.Context(), "RemoveCatalogEntry", []any{kind, name, harness}, nil)
			case "enable", "disable":
				err = engineClient().Call(cmd.Context(), "SetCatalogEntryEnabled", []any{kind, name, harness, verb == "enable"}, nil)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Dyna catalog updated.")
			return err
		}
		parent.AddCommand(c)
	}
}
