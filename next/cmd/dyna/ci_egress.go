package main

import (
	"errors"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/resident-labs/dyna/box/egress"
	"github.com/spf13/cobra"
)

// ciEgressCommand is the proxy of a CI host. It runs in a container the pool starts.
func ciEgressCommand() *cobra.Command {
	var socket string
	cmd := &cobra.Command{Use: "ci-egress", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		listener, err := net.Listen("unix", socket)
		if err != nil {
			return err
		}
		return egress.Serve(ctx, listener, egress.PublicDial())
	}}
	cmd.Flags().StringVar(&socket, "socket", "", "Unix socket to serve")
	_ = cmd.MarkFlagRequired("socket") // fails only for an undefined flag
	return cmd
}
