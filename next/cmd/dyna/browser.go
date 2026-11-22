package main

import (
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func browserCommand(getClient func() client) *cobra.Command {
	browser := &cobra.Command{
		Use:   "browser",
		Short: "Manage owner-level shared browser profiles",
		Long:  "Manage owner-level shared Chromium profiles. Assign a profile to a room with `dyna room browser ROOM NAME|none`; one session holds a profile at a time.",
	}
	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a shared browser profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := getClient().BrowserCreate(cmd.Context(), contract.BrowserCreateRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List shared browser profiles, their rooms, and active leases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := getClient().BrowserList(cmd.Context(), contract.BrowserListRequest{})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
		},
	}
	remove := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an unused, unleased shared browser profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := getClient().BrowserRemove(cmd.Context(), contract.BrowserRemoveRequest{Name: args[0]})
			return err
		},
	}
	release := &cobra.Command{
		Use:   "release NAME",
		Short: "Forcibly release a stuck lease on a shared browser profile",
		Long:  "Release a shared browser profile lease held by a stuck or unreachable box, discarding that box's unsaved changes and bumping the profile generation so any late checkin is refused.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := getClient().BrowserRelease(cmd.Context(), contract.BrowserReleaseRequest{Name: args[0]})
			if err != nil {
				return err
			}
			if res.Released {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "released %s (generation %d)\n", args[0], res.Generation)
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s was not leased (generation %d)\n", args[0], res.Generation)
			return err
		},
	}
	browser.AddCommand(create, list, remove, release)
	return browser
}
