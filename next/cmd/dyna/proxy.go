package main

import (
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/spf13/cobra"
)

func proxyCommand() *cobra.Command {
	var path string
	cmd := &cobra.Command{Use: "proxy", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadConfig(path)
		if err != nil {
			return err
		}
		file := os.NewFile(3, "proxy.sock")
		listener, err := net.FileListener(file)
		_ = file.Close()
		if err != nil {
			return err
		}
		defer listener.Close()
		service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).WithListener(listener).Build()
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM)
		defer cancel()
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
		return service.Run(ctx)
	}}
	cmd.Flags().StringVar(&path, "config", "", "Proxy config path")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}
