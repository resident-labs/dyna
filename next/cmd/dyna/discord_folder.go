package main

import (
	"context"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func (c *socketClient) DiscordExport(ctx context.Context, req contract.DiscordExportRequest) (contract.DiscordFolderResponse, error) {
	return call[contract.DiscordExportRequest, contract.DiscordFolderResponse](ctx, c, contract.MethodDiscordExport, req)
}
func (c *socketClient) DiscordPlan(ctx context.Context, req contract.DiscordPlanRequest) (contract.DiscordFolderResponse, error) {
	return call[contract.DiscordPlanRequest, contract.DiscordFolderResponse](ctx, c, contract.MethodDiscordPlan, req)
}

func (c *socketClient) DiscordApply(ctx context.Context, req contract.DiscordApplyRequest) (contract.DiscordFolderResponse, error) {
	return call[contract.DiscordApplyRequest, contract.DiscordFolderResponse](ctx, c, contract.MethodDiscordApply, req)
}

func discordFolderCommands(getClient func() client) []*cobra.Command {
	export := &cobra.Command{Use: "export [ROOM]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, ok := getClient().(interface {
			DiscordExport(context.Context, contract.DiscordExportRequest) (contract.DiscordFolderResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		room, _ := cmd.Flags().GetString("room")
		if len(args) > 0 {
			if room != "" {
				return fmt.Errorf("name room once")
			}
			room = args[0]
		}
		server, _ := cmd.Flags().GetString("server")
		force, _ := cmd.Flags().GetBool("force")
		result, err := service.DiscordExport(cmd.Context(), contract.DiscordExportRequest{Room: contract.RoomID(room), Server: server, Force: force})
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), result.Text)
		}
		return err
	}}
	export.Flags().Bool("force", false, "Replace an existing folder, preserving unmanaged areas")
	plan := &cobra.Command{Use: "plan [ROOM]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, ok := getClient().(interface {
			DiscordPlan(context.Context, contract.DiscordPlanRequest) (contract.DiscordFolderResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		room, _ := cmd.Flags().GetString("room")
		if len(args) > 0 {
			if room != "" {
				return fmt.Errorf("name room once")
			}
			room = args[0]
		}
		server, _ := cmd.Flags().GetString("server")
		result, err := service.DiscordPlan(cmd.Context(), contract.DiscordPlanRequest{Room: contract.RoomID(room), Server: server})
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), result.Text)
		}
		return err
	}}
	apply := &cobra.Command{Use: "apply [ROOM]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, ok := getClient().(interface {
			DiscordApply(context.Context, contract.DiscordApplyRequest) (contract.DiscordFolderResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		room, _ := cmd.Flags().GetString("room")
		if len(args) > 0 {
			if room != "" {
				return fmt.Errorf("name room once")
			}
			room = args[0]
		}
		server, _ := cmd.Flags().GetString("server")
		prune, _ := cmd.Flags().GetBool("prune")
		result, err := service.DiscordApply(cmd.Context(), contract.DiscordApplyRequest{Room: contract.RoomID(room), Server: server, Prune: prune})
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), result.Text)
		}
		return err
	}}
	apply.Flags().Bool("prune", false, "Also delete objects apply owns that the folder no longer lists")
	for _, command := range []*cobra.Command{export, plan, apply} {
		command.Flags().String("room", "", "Room name or ID (required for owner callers)")
		command.Flags().String("server", "", "Mapped server ID")
	}
	return []*cobra.Command{export, plan, apply}
}
