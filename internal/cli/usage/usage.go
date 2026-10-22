// Package usage shows the Usage page's account windows to agents and the owner.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/account/service"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/engineclient"
	"github.com/spf13/cobra"
)

type Options struct {
	Store  *store.Store
	Client *engineclient.Client
}

func NewCommand() *cobra.Command { return Command(Options{}) }
func Command(o Options) *cobra.Command {
	if o.Store == nil {
		o.Store = store.DefaultStore()
	}
	if o.Client == nil {
		o.Client = engineclient.New("")
	}
	var harness string
	var refresh, asJSON bool
	cmd := &cobra.Command{Use: "usage", Short: "Show usage windows of the saved accounts", Long: "Show usage windows of the saved accounts. The default reads the cached observation. --refresh asks each provider for the saved accounts' current usage, like the Usage page.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var snap service.Snapshot
		if refresh {
			ctx, cancel := context.WithTimeout(c.Context(), 55*time.Second)
			defer cancel()
			if err := o.Client.Call(ctx, "AccountUsage", []any{true}, &snap); err != nil {
				return err
			}
		} else {
			snap = service.CachedUsage(o.Store, time.Now())
		}
		if snap.Message != "" {
			return errors.New(snap.Message)
		}
		rows := snap.Accounts[:0:0]
		for _, a := range snap.Accounts {
			if harness == "" || a.Harness == harness {
				rows = append(rows, a)
			}
		}
		snap.Accounts = rows
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(snap)
		}
		for _, a := range rows {
			mark := " "
			if a.Active {
				mark = "*"
			}
			stale := ""
			if a.Stale {
				stale = "  (stale)"
			}
			age := ""
			if !a.ObservedAt.IsZero() {
				age = fmt.Sprintf("  (observed %s ago)", max(time.Duration(0), time.Since(a.ObservedAt)).Truncate(time.Second))
			}
			blocked := ""
			if a.QuotaBlocked && a.QuotaBlockedUntil != nil {
				blocked = "  blocked until " + a.QuotaBlockedUntil.Local().Format("Mon 3:04 PM")
			}
			fmt.Fprintf(c.OutOrStdout(), "%s %s  %s  %s%s%s%s\n", mark, a.Harness, a.Label, service.WindowsSummary(a), blocked, age, stale)
		}
		return nil
	}}
	cmd.Flags().StringVar(&harness, "harness", "", "only this harness, for example codex")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ask the providers for current usage")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
