package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
)

func (c *socketClient) DiscordAPI(ctx context.Context, req contract.DiscordAPIRequest) (contract.DiscordAPIResponse, error) {
	return call[contract.DiscordAPIRequest, contract.DiscordAPIResponse](ctx, c, contract.MethodDiscordAPI, req)
}
func (c *socketClient) DiscordRead(ctx context.Context, req contract.DiscordReadRequest) (contract.DiscordReadResponse, error) {
	return call[contract.DiscordReadRequest, contract.DiscordReadResponse](ctx, c, contract.MethodDiscordRead, req)
}
func (c *socketClient) DiscordSend(ctx context.Context, req contract.DiscordSendRequest) (contract.DiscordSendResponse, error) {
	return call[contract.DiscordSendRequest, contract.DiscordSendResponse](ctx, c, contract.MethodDiscordSend, req)
}
func (c *socketClient) DiscordConfigure(ctx context.Context, req contract.DiscordConfigureRequest) (contract.DiscordConfigureResponse, error) {
	return call[contract.DiscordConfigureRequest, contract.DiscordConfigureResponse](ctx, c, contract.MethodDiscordConfigure, req)
}
func discordCommand(getClient func() client) *cobra.Command {
	var owner string
	var clear bool
	root := &cobra.Command{Use: "discord", Short: "Show or configure the machine's Discord bot", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.DiscordConfigureRequest{Clear: clear}
		if cmd.Flags().Changed("owner") {
			req.Owner = &owner
			if clear {
				return fmt.Errorf("clear cannot include owner")
			}
			input := cmd.InOrStdin()
			read := true
			if file, ok := input.(*os.File); ok {
				info, err := file.Stat()
				if err != nil {
					return err
				}
				read = info.Mode()&os.ModeCharDevice == 0
			}
			if read {
				data, err := io.ReadAll(io.LimitReader(input, 16385))
				if err != nil {
					return fmt.Errorf("cannot read token")
				}
				if len(data) > 16384 {
					return fmt.Errorf("token is too long")
				}
				req.Token = strings.TrimSpace(string(data))
			}
		}
		service, ok := getClient().(interface {
			DiscordConfigure(context.Context, contract.DiscordConfigureRequest) (contract.DiscordConfigureResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		result, err := service.DiscordConfigure(cmd.Context(), req)
		if err != nil {
			return err
		}
		if req.Clear || req.Token != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "If tool availability changed, run dyna session refresh SESSION for the answering sessions and ancestors that are already running. Instructions update live.")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	root.Flags().StringVar(&owner, "owner", "", "Owner's Discord user ID; optional bot token on stdin")
	root.Flags().BoolVar(&clear, "clear", false, "Remove the machine's bot")
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
	var readReq contract.DiscordReadRequest
	read := &cobra.Command{Use: "read CHANNEL", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, ok := getClient().(interface {
			DiscordRead(context.Context, contract.DiscordReadRequest) (contract.DiscordReadResponse, error)
		})
		if !ok {
			return fmt.Errorf("Discord unavailable")
		}
		readReq.Channel = args[0]
		result, err := service.DiscordRead(cmd.Context(), readReq)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	read.Flags().IntVar(&readReq.Limit, "limit", 20, "Message count (maximum 100)")
	read.Flags().StringVar(&readReq.Message, "message", "", "Read one message and retrieve its files")
	read.Flags().StringVar(&readReq.Before, "before", "", "Messages before this ID")
	read.Flags().StringVar(&readReq.After, "after", "", "Messages after this ID")
	read.Flags().StringVar(&readReq.Around, "around", "", "Messages around this ID")
	root.AddCommand(api, send, read)
	return root
}
