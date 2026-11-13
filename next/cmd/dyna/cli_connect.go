package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func connectCommand(getClient func() client) *cobra.Command {
	connect := &cobra.Command{Use: "connect"}
	type connectClient interface {
		ConnectStart(context.Context, contract.ConnectStartRequest) (contract.ConnectStartResponse, error)
		ConnectStatus(context.Context, contract.ConnectStatusRequest) (contract.ConnectStatusResponse, error)
		ConnectCancel(context.Context, contract.ConnectCancelRequest) (contract.ConnectCancelResponse, error)
		ConnectUnlink(context.Context, contract.ConnectUnlinkRequest) (contract.ConnectUnlinkResponse, error)
	}
	getConnect := func() (connectClient, error) {
		c, ok := getClient().(connectClient)
		if !ok {
			return nil, fmt.Errorf("Connect unavailable")
		}
		return c, nil
	}
	var connectRelay, connectName string
	connectStart := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectStart(cmd.Context(), contract.ConnectStartRequest{Relay: connectRelay, Name: connectName})
		return err
	}}
	connectStart.Flags().StringVar(&connectRelay, "relay", "", "Relay origin")
	connectStart.Flags().StringVar(&connectName, "name", "", "Machine name")
	connect.AddCommand(connectStart, &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		status, err := c.ConnectStatus(cmd.Context(), contract.ConnectStatusRequest{})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}, &cobra.Command{Use: "cancel", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectCancel(cmd.Context(), contract.ConnectCancelRequest{})
		return err
	}}, &cobra.Command{Use: "unlink", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectUnlink(cmd.Context(), contract.ConnectUnlinkRequest{})
		return err
	}})
	return connect
}
