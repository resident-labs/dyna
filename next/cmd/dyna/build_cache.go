package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/resident-labs/dyna/box/buildcache"
	"github.com/spf13/cobra"
)

// buildCacheCommand serves a host's compiler cache to its boxes and CI runners. It is not for people to run.
func buildCacheCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "build-cache", Hidden: true}
	var dir, listen string
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return buildcache.Serve(ctx, dir, listen)
	}}
	serve.Flags().StringVar(&dir, "dir", "", "The folder of the store")
	serve.Flags().StringVar(&listen, "listen", "", "The Unix socket to serve on")
	cmd.AddCommand(serve)
	return cmd
}
