// Package approvals exposes owner approval settings.
package approvals

import (
	"context"
	"fmt"
	"time"

	core "dyna-agent/internal/approvals"
	"dyna-agent/internal/engineclient"
	"github.com/spf13/cobra"
)

// Client is the engine call transport.
type Client interface {
	Call(context.Context, string, []any, any) error
}

// Options replaces the engine transport in tests.
type Options struct{ Client Client }

func NewCommand() *cobra.Command { return Command(Options{}) }

func Command(o Options) *cobra.Command {
	if o.Client == nil {
		o.Client = engineclient.New("")
	}
	var roomRef string
	root := &cobra.Command{
		Use:   "approvals",
		Short: "Show and change agent write approval settings",
		Long:  "Pending approvals are decided with dyna request approve or deny.\nThis command shows and changes the approval wait and expiry settings.",
		Args:  cobra.NoArgs,
	}
	root.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (defaults to your room)")
	var wait, expiry time.Duration
	settings := &cobra.Command{Use: "settings", Short: "Show or set the approval wait and expiry", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var current core.Settings
		if err := o.Client.Call(c.Context(), "ApprovalSettings", []any{core.SettingsInput{Room: roomRef}}, &current); err != nil {
			return err
		}
		changed := c.Flags().Changed("wait") || c.Flags().Changed("expiry")
		if changed {
			if c.Flags().Changed("wait") {
				current.Wait = wait
			}
			if c.Flags().Changed("expiry") {
				current.Expiry = expiry
			}
			if err := o.Client.Call(c.Context(), "ApprovalSettings", []any{core.SettingsInput{Room: roomRef, Wait: current.Wait, Expiry: current.Expiry, Set: true}}, &current); err != nil {
				return err
			}
		}
		fmt.Fprintf(c.OutOrStdout(), "wait %s\nexpiry %s\n", current.Wait, current.Expiry)
		return nil
	}}
	settings.Flags().DurationVar(&wait, "wait", core.DefaultWait, "how long the caller waits for a result")
	settings.Flags().DurationVar(&expiry, "expiry", core.DefaultExpiry, "how long a pending request waits before it expires")
	var since time.Duration
	log := &cobra.Command{Use: "log", Short: "List inline writes and approval decisions", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var entries []core.LogEntry
		if err := o.Client.Call(c.Context(), "ApprovalLog", []any{roomRef, since}, &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %s  %s", entry.Time.Format(time.RFC3339), entry.Actor, entry.Kind, entry.Target, entry.State)
			if entry.ExitCode != nil {
				fmt.Fprintf(c.OutOrStdout(), "  exit %d", *entry.ExitCode)
			}
			if entry.Detail != "" {
				fmt.Fprintf(c.OutOrStdout(), "  %s", entry.Detail)
			}
			fmt.Fprintln(c.OutOrStdout())
		}
		return nil
	}}
	log.Flags().DurationVar(&since, "since", 24*time.Hour, "show activity since this duration ago")
	root.AddCommand(settings, log)
	return root
}
