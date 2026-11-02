package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// addonFlags are the flags of every change: the room is the default, and in a box it is the box's own.
type addonFlags struct {
	room   string
	global bool
}

func (f *addonFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.room, "room", "", "the room, which a box already knows")
	cmd.Flags().BoolVar(&f.global, "global", false, "every room; a session's request waits for the owner")
}

func describeChange(cmd *cobra.Command, result contract.AddonChangeResponse) {
	if result.Operation != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "waiting for the owner:", result.Operation)
		return
	}
	if result.Item != nil {
		fmt.Fprintln(cmd.OutOrStdout(), result.Item.Name, "reaches each session at its next box start")
	}
}

func describeAddon(item contract.Addon) string {
	scope := "room " + string(item.Room)
	switch {
	case item.BuiltIn:
		scope = "built-in"
	case item.Room == "":
		scope = "global"
	}
	what := item.Source
	switch {
	case item.URL != "":
		what = item.URL
	case item.Command != "":
		what = strings.Join(append([]string{item.Command}, item.Args...), " ")
	case what == "":
		what = item.Description
	}
	state := "on"
	if item.Disabled {
		state = "off"
	}
	return strings.Join([]string{item.Name, scope, state, item.By, what}, "\t")
}

// addonList and addonRemove are the same two commands for each kind.
func addonList(get func() *socketClient, method contract.Method) *cobra.Command {
	var room string
	cmd := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.AddonListRequest, contract.AddonListResponse](cmd.Context(), get(), method, contract.AddonListRequest{Room: contract.RoomID(room)})
		if err != nil {
			return err
		}
		for _, item := range result.Items {
			fmt.Fprintln(cmd.OutOrStdout(), describeAddon(item))
		}
		return nil
	}}
	cmd.Flags().StringVar(&room, "room", "", "the room, which a box already knows")
	return cmd
}

func addonRemove(get func() *socketClient, method contract.Method) *cobra.Command {
	var flags addonFlags
	cmd := &cobra.Command{Use: "remove NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := call[contract.AddonRemoveRequest, contract.AddonChangeResponse](cmd.Context(), get(), method, contract.AddonRemoveRequest{Name: args[0], Room: contract.RoomID(flags.room), Global: flags.global})
		if err != nil {
			return err
		}
		if result.Operation == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "removed", args[0])
		}
		describeChange(cmd, result)
		return nil
	}}
	flags.bind(cmd)
	return cmd
}

func addonCommands(get func() *socketClient) []*cobra.Command {
	change := func(ctx context.Context, cmd *cobra.Command, send func(context.Context) (contract.AddonChangeResponse, error)) error {
		result, err := send(ctx)
		if err == nil {
			describeChange(cmd, result)
		}
		return err
	}

	pkg := &cobra.Command{Use: "package", Short: "Pi packages: tools, skills and prompts Pi installs from npm or git"}
	var packageFlags addonFlags
	packageAdd := &cobra.Command{Use: "add SOURCE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return change(cmd.Context(), cmd, func(ctx context.Context) (contract.AddonChangeResponse, error) {
			return call[contract.PackageAddRequest, contract.AddonChangeResponse](ctx, get(), contract.MethodPackageAdd, contract.PackageAddRequest{Source: args[0], Room: contract.RoomID(packageFlags.room), Global: packageFlags.global})
		})
	}}
	packageFlags.bind(packageAdd)
	pkg.AddCommand(packageAdd, addonList(get, contract.MethodPackageList), addonRemove(get, contract.MethodPackageRemove))

	skill := &cobra.Command{Use: "skill", Short: "Skills: instructions an agent reads when they apply"}
	var skillFlags addonFlags
	skillSave := &cobra.Command{Use: "save PATH", Short: "Save a folder with SKILL.md, or a link to one", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req, err := skills.Request(args[0], "", contract.RoomID(skillFlags.room), skillFlags.global)
		if err != nil {
			return err
		}
		return change(cmd.Context(), cmd, func(ctx context.Context) (contract.AddonChangeResponse, error) {
			return call[contract.SkillSaveRequest, contract.AddonChangeResponse](ctx, get(), contract.MethodSkillSave, req)
		})
	}}
	skillFlags.bind(skillSave)
	skill.AddCommand(skillSave, addonList(get, contract.MethodSkillList), addonRemove(get, contract.MethodSkillRemove))

	mcp := &cobra.Command{Use: "mcp", Short: "MCP servers, beside dyna and shell"}
	var mcpFlags addonFlags
	mcpAdd := &cobra.Command{Use: "add NAME URL_OR_COMMAND [ARGS...]", Short: "Add a server. Put -- before a command with flags of its own.", Args: cobra.MinimumNArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.MCPAddRequest{Name: args[0], Room: contract.RoomID(mcpFlags.room), Global: mcpFlags.global}
		if strings.HasPrefix(args[1], "https://") || strings.HasPrefix(args[1], "http://") {
			req.URL = args[1]
		} else {
			req.Command = args[1]
		}
		req.Args = args[2:]
		return change(cmd.Context(), cmd, func(ctx context.Context) (contract.AddonChangeResponse, error) {
			return call[contract.MCPAddRequest, contract.AddonChangeResponse](ctx, get(), contract.MethodMCPAdd, req)
		})
	}}
	mcpFlags.bind(mcpAdd)
	mcp.AddCommand(mcpAdd, addonList(get, contract.MethodMCPList), addonRemove(get, contract.MethodMCPRemove))
	return []*cobra.Command{pkg, skill, mcp}
}
