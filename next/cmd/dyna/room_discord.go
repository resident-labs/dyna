package main

import (
	"context"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
	"io"
	"strings"
)

func (c *socketClient) RoomDiscord(ctx context.Context, req contract.RoomDiscordRequest) (contract.RoomDiscordResponse, error) {
	return call[contract.RoomDiscordRequest, contract.RoomDiscordResponse](ctx, c, contract.MethodRoomDiscord, req)
}
func roomDiscordCommand(getClient func() client) *cobra.Command {
	var session string
	var clear bool
	cmd := &cobra.Command{Use: "discord ROOM", Short: "Show, connect or clear a room's Discord bot", Long: "Connect with --session NAME and the bot token on stdin. For public servers, use a cheap front child of root with no repository or host access. Root answering directly is suitable for private servers. The bot must belong to exactly one server.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.RoomDiscordRequest{Room: contract.RoomID(args[0]), Clear: clear}
		if clear && cmd.Flags().Changed("session") {
			return fmt.Errorf("clear cannot include session")
		}
		if !clear && cmd.Flags().Changed("session") {
			req.Session = session
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 16385))
			if err != nil {
				return fmt.Errorf("cannot read token from stdin")
			}
			if len(data) > 16384 {
				return fmt.Errorf("token is too long")
			}
			req.Token = strings.TrimSpace(string(data))
			if req.Token == "" {
				return fmt.Errorf("token on stdin is empty")
			}
		}
		service, ok := getClient().(interface {
			RoomDiscord(context.Context, contract.RoomDiscordRequest) (contract.RoomDiscordResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		result, err := service.RoomDiscord(cmd.Context(), req)
		if err != nil {
			return err
		}
		if result.Guild == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Discord: not connected")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Discord: bot %s in server %s, answered by %s\n", result.BotName, result.GuildName, result.Session)
		}
		return nil
	}}
	cmd.Flags().StringVar(&session, "session", "root", "Answering session (default root); reads the token on stdin")
	cmd.Flags().BoolVar(&clear, "clear", false, "Remove the Discord connection")
	return cmd
}
