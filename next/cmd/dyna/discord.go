package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
	"io"
)

func (c *socketClient) DiscordAPI(ctx context.Context, req contract.DiscordAPIRequest) (contract.DiscordAPIResponse, error) {
	return call[contract.DiscordAPIRequest, contract.DiscordAPIResponse](ctx, c, contract.MethodDiscordAPI, req)
}
func (c *socketClient) DiscordSend(ctx context.Context, req contract.DiscordSendRequest) (contract.DiscordSendResponse, error) {
	return call[contract.DiscordSendRequest, contract.DiscordSendResponse](ctx, c, contract.MethodDiscordSend, req)
}
func discordCommand(getClient func() client) *cobra.Command {
	root := &cobra.Command{Use: "discord", Short: "Use the room's Discord bot from its answering session or an ancestor"}
	api := &cobra.Command{Use: "api METHOD PATH", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		service, ok := getClient().(interface {
			DiscordAPI(context.Context, contract.DiscordAPIRequest) (contract.DiscordAPIResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		var body json.RawMessage
		if args[0] != "GET" {
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
			if err != nil {
				return err
			}
			body = data
		}
		result, err := service.DiscordAPI(cmd.Context(), contract.DiscordAPIRequest{Method: args[0], Path: args[1], Body: body})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	send := &cobra.Command{Use: "send CHANNEL TEXT", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		service, ok := getClient().(interface {
			DiscordSend(context.Context, contract.DiscordSendRequest) (contract.DiscordSendResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		result, err := service.DiscordSend(cmd.Context(), contract.DiscordSendRequest{Channel: args[0], Text: args[1]})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	root.AddCommand(api, send)
	return root
}
