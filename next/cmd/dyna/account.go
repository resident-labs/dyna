package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func accountCommand(get func() client) *cobra.Command {
	root := &cobra.Command{Use: "account", Short: "Model accounts (owner only)"}
	root.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := get().AccountsList(cmd.Context(), contract.AccountsListRequest{})
		if err != nil {
			return err
		}
		for _, a := range r.Accounts {
			rooms, prefer := "all", "none"
			if len(a.Rooms) > 0 {
				names := make([]string, len(a.Rooms))
				for i, r := range a.Rooms {
					names[i] = string(r)
				}
				rooms = strings.Join(names, ",")
			}
			if len(a.Preferred) > 0 {
				names := make([]string, len(a.Preferred))
				for i, r := range a.Preferred {
					names[i] = string(r)
				}
				prefer = strings.Join(names, ",")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\trooms=%s\tprefer=%s\n", a.ID, a.Label, a.Provider, rooms, prefer)
		}
		return nil
	}}, &cobra.Command{Use: "quota", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := get().AccountsQuota(cmd.Context(), contract.AccountsQuotaRequest{})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}})
	var rooms, prefer string
	limit := &cobra.Command{Use: "limit ACCOUNT [--rooms ROOM,…|all] [--prefer ROOM,…|none]", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("rooms") && !cmd.Flags().Changed("prefer") {
			return fmt.Errorf("give --rooms or --prefer\n%s", cmd.UsageString())
		}
		a, err := findAccount(cmd, get(), args[0])
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("rooms") {
			a.Rooms = nil
			if rooms != "all" {
				for _, r := range strings.Split(rooms, ",") {
					a.Rooms = append(a.Rooms, contract.RoomID(r))
				}
			}
		}
		if cmd.Flags().Changed("prefer") {
			a.Preferred = nil
			if prefer != "none" {
				for _, r := range strings.Split(prefer, ",") {
					a.Preferred = append(a.Preferred, contract.RoomID(r))
				}
			}
		}
		_, err = get().AccountsSetLimits(cmd.Context(), contract.AccountsSetLimitsRequest{AccountID: a.ID, Rooms: a.Rooms, Preferred: a.Preferred})
		return err
	}}
	limit.Flags().StringVar(&rooms, "rooms", "", "rooms served, or all")
	limit.Flags().StringVar(&prefer, "prefer", "", "rooms that prefer this account, or none")
	root.AddCommand(limit)
	return root
}

func findAccount(cmd *cobra.Command, c client, name string) (contract.Account, error) {
	r, err := c.AccountsList(cmd.Context(), contract.AccountsListRequest{})
	if err != nil {
		return contract.Account{}, err
	}
	var found []contract.Account
	for _, a := range r.Accounts {
		if string(a.ID) == name {
			return a, nil
		}
		if a.Label == name {
			found = append(found, a)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return contract.Account{}, fmt.Errorf("account %q matches %d labels; use an ID from account list", name, len(found))
}
func setAccountCommand(get func() client) *cobra.Command {
	return &cobra.Command{Use: "set-account SESSION ACCOUNT|auto", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		var id contract.AccountID
		if args[1] != "auto" {
			a, err := findAccount(cmd, get(), args[1])
			if err != nil {
				return err
			}
			id = a.ID
		}
		_, err := get().SessionSetAccount(cmd.Context(), contract.SessionSetAccountRequest{Session: contract.SessionID(args[0]), Account: id})
		return err
	}}
}
func addonCommand(get func() client) *cobra.Command {
	root := &cobra.Command{Use: "addon", Short: "Change an added skill, package or MCP server (owner only)"}
	var room, enabled, rootOnly string
	set := &cobra.Command{Use: "set KIND NAME [--room ROOM] [--enabled on|off] [--root-only on|off]", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		// Parse both switches before making either change.
		var en, ro *bool
		var err error
		if cmd.Flags().Changed("enabled") {
			en, err = hostSwitch("enabled", enabled)
			if err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("root-only") {
			ro, err = hostSwitch("root-only", rootOnly)
			if err != nil {
				return err
			}
		}
		kind := contract.AddonKind(args[0])
		if kind != contract.AddonSkill && kind != contract.AddonPackage && kind != contract.AddonMCP {
			return fmt.Errorf("KIND must be skill, package or mcp")
		}
		if en != nil {
			_, err = get().AddonSetEnabled(cmd.Context(), contract.AddonSetEnabledRequest{Kind: kind, Name: args[1], Room: contract.RoomID(room), Enabled: *en})
			if err != nil {
				return err
			}
		}
		if ro != nil {
			_, err = get().AddonSetRootOnly(cmd.Context(), contract.AddonSetRootOnlyRequest{Kind: kind, Name: args[1], Room: contract.RoomID(room), RootOnly: *ro})
		}
		return err
	}}
	set.Flags().StringVar(&room, "room", "", "room scope; omitted means global")
	set.Flags().StringVar(&enabled, "enabled", "", "on or off")
	set.Flags().StringVar(&rootOnly, "root-only", "", "on or off")
	root.AddCommand(set)
	return root
}

func (c *socketClient) AccountsList(ctx context.Context, q contract.AccountsListRequest) (contract.AccountsListResponse, error) {
	return call[contract.AccountsListRequest, contract.AccountsListResponse](ctx, c, contract.MethodAccountsList, q)
}

func (c *socketClient) AccountsQuota(ctx context.Context, q contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, error) {
	return call[contract.AccountsQuotaRequest, contract.AccountsQuotaResponse](ctx, c, contract.MethodAccountsQuota, q)
}

func (c *socketClient) AccountsSetLimits(ctx context.Context, q contract.AccountsSetLimitsRequest) (contract.AccountsSetLimitsResponse, error) {
	return call[contract.AccountsSetLimitsRequest, contract.AccountsSetLimitsResponse](ctx, c, contract.MethodAccountsSetLimits, q)
}

func (c *socketClient) SessionSetAccount(ctx context.Context, q contract.SessionSetAccountRequest) (contract.SessionSetAccountResponse, error) {
	return call[contract.SessionSetAccountRequest, contract.SessionSetAccountResponse](ctx, c, contract.MethodSessionSetAccount, q)
}

func (c *socketClient) AddonSetEnabled(ctx context.Context, q contract.AddonSetEnabledRequest) (contract.AddonSetEnabledResponse, error) {
	return call[contract.AddonSetEnabledRequest, contract.AddonSetEnabledResponse](ctx, c, contract.MethodAddonSetEnabled, q)
}

func (c *socketClient) AddonSetRootOnly(ctx context.Context, q contract.AddonSetRootOnlyRequest) (contract.AddonSetRootOnlyResponse, error) {
	return call[contract.AddonSetRootOnlyRequest, contract.AddonSetRootOnlyResponse](ctx, c, contract.MethodAddonSetRootOnly, q)
}
