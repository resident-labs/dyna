package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
	"io"
	"os"
	"unicode/utf8"
)

func (c *socketClient) RoomDiscord(ctx context.Context, req contract.RoomDiscordRequest) (contract.RoomDiscordResponse, error) {
	return call[contract.RoomDiscordRequest, contract.RoomDiscordResponse](ctx, c, contract.MethodRoomDiscord, req)
}
func roomDiscordCommand(getClient func() client) *cobra.Command {
	var server, channel, session, instructionsFile string
	var remove bool
	cmd := &cobra.Command{Use: "discord ROOM", Short: "Map a Discord server or channel to a room", Long: "Map with --server ID and optionally --channel ID, --session NAME (default root), and --instructions-file FILE. For public servers use a cheap front child of root with no repository or host access. Start it without a goal: it has no finish line, and an idle session with an open goal is reported as stalled. No flags lists this room's mappings.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.RoomDiscordRequest{Room: contract.RoomID(args[0]), Server: server, Channel: channel, Remove: remove}
		if cmd.Flags().Changed("session") {
			req.Session = session
		}
		if instructionsFile != "" {
			input := cmd.InOrStdin()
			if instructionsFile != "-" {
				file, err := os.Open(instructionsFile)
				if err != nil {
					return err
				}
				defer file.Close()
				input = file
			}
			data, err := io.ReadAll(io.LimitReader(input, 16001))
			if err != nil {
				return err
			}
			if !utf8.Valid(data) || utf8.RuneCount(data) > 4000 {
				return fmt.Errorf("instructions must be valid UTF-8, at most 4,000 characters")
			}
			text := string(data)
			req.Instructions = &text
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
		if req.Server != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "If tool availability changed, run dyna session refresh SESSION for the answering session and its ancestors that are already running. Instructions update live.")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&server, "server", "", "Discord server ID")
	cmd.Flags().StringVar(&channel, "channel", "", "Optional channel ID")
	cmd.Flags().StringVar(&session, "session", "root", "Answering session")
	cmd.Flags().StringVar(&instructionsFile, "instructions-file", "", "Owner's instructions for this mapping, at most 4,000 characters")
	cmd.Flags().BoolVar(&remove, "remove", false, "Unmap this server or channel")
	return cmd
}
