// Package sync exposes quiet local lane sync commands.
package sync

import (
	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/lanesync"
	"fmt"
	"github.com/spf13/cobra"
	"sort"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "Fetch and inspect local lane remotes"}
	var asJSON bool
	show := func(c *cobra.Command, status lanesync.Status) error {
		if asJSON {
			return jsonout.Write(c.OutOrStdout(), status)
		}
		repos := make([]string, 0, len(status))
		for repo := range status {
			repos = append(repos, repo)
		}
		sort.Strings(repos)
		for _, repo := range repos {
			names := make([]string, 0, len(status[repo].Remotes))
			for name := range status[repo].Remotes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				r := status[repo].Remotes[name]
				fmt.Fprintf(c.OutOrStdout(), "%s %s: %d ahead, %d behind", repo, name, r.Ahead, r.Behind)
				if r.LastError != "" {
					fmt.Fprintf(c.OutOrStdout(), " (%s)", r.LastError)
				}
				fmt.Fprintln(c.OutOrStdout())
			}
		}
		return nil
	}
	status := &cobra.Command{Use: "status", Short: "Show saved ahead and behind counts", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		saved, err := lanesync.New().Status()
		if err != nil {
			return err
		}
		return show(c, saved)
	}}
	status.Flags().BoolVar(&asJSON, "json", false, "print JSON status")
	fetch := &cobra.Command{Use: "fetch", Short: "Fetch all local lane remotes now", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var saved lanesync.Status
		if err := engineclient.New("").Call(c.Context(), "SyncFetch", nil, &saved); err != nil {
			return err
		}
		return show(c, saved)
	}}
	cmd.AddCommand(status, fetch, OperationCommand("refresh", "SyncRefresh", "Merge origin/main into integration after local CI passes"))
	return cmd
}
