package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func (c *socketClient) RoomGitHub(ctx context.Context, req contract.RoomGitHubRequest) (contract.RoomGitHubResponse, error) {
	return call[contract.RoomGitHubRequest, contract.RoomGitHubResponse](ctx, c, contract.MethodRoomGitHub, req)
}
func roomGitHubCommand(getClient func() client) *cobra.Command {
	var name, email string
	var clear bool
	cmd := &cobra.Command{Use: "github ROOM", Short: "Show, replace or clear a room's GitHub account; replacement token comes from stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.RoomGitHubRequest{Room: contract.RoomID(args[0]), Name: name, Email: email, Clear: clear}
		if clear && (name != "" || email != "") {
			return fmt.Errorf("clear cannot include name or email")
		}
		if !clear && (name != "" || email != "") {
			if name == "" || email == "" {
				return fmt.Errorf("both name and email are required")
			}
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
			RoomGitHub(context.Context, contract.RoomGitHubRequest) (contract.RoomGitHubResponse, error)
		})
		if !ok {
			return fmt.Errorf("GitHub account command unavailable")
		}
		result, err := service.RoomGitHub(cmd.Context(), req)
		if err != nil {
			return err
		}
		if result.Login == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "GitHub account: engine default")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "GitHub account: %s\nName: %s\nEmail: %s\n", result.Login, result.Name, result.Email)
		}
		return nil
	}}
	cmd.Flags().StringVar(&name, "name", "", "Commit author name")
	cmd.Flags().StringVar(&email, "email", "", "Commit author email")
	cmd.Flags().BoolVar(&clear, "clear", false, "Return to the engine's GitHub account")
	return cmd
}
