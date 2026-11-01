package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/resident-labs/dyna/box/share"
	"github.com/spf13/cobra"
)

// shareCommand serves the rooms' data on the engine host and mounts it on the others. Neither is for people to run.
func shareCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "share", Hidden: true}
	var rooms, listen, server, dir, cache string
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return share.Serve(ctx, rooms, listen)
	}}
	serve.Flags().StringVar(&rooms, "rooms", "", "The folder of every room")
	serve.Flags().StringVar(&listen, "listen", "", "The Unix socket to serve on")
	mount := &cobra.Command{Use: "mount", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return share.Mount(cmd.Context(), server, dir, cache)
	}}
	mount.Flags().StringVar(&server, "server", "", "The forwarded Unix socket of the server")
	mount.Flags().StringVar(&dir, "dir", "", "The folder to mount at")
	mount.Flags().StringVar(&cache, "cache", "", "The folder of the local cache")
	cmd.AddCommand(serve, mount)
	return cmd
}
