package main

import (
	"encoding/json"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func browserCommand(getClient func() client) *cobra.Command {
	browser := &cobra.Command{
		Use:   "browser",
		Short: "Manage owner-level shared browser profiles",
		Long:  "Manage owner-level shared browser cookie jars. Assign a profile to a room with `dyna room browser ROOM NAME|none`.",
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
		Short: "List shared browser profiles, their rooms, and cookie counts",
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
		Short: "Remove an unused shared browser profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := getClient().BrowserRemove(cmd.Context(), contract.BrowserRemoveRequest{Name: args[0]})
			return err
		},
	}
	browser.AddCommand(create, list, remove)
	return browser
}
