package main

import (
	"context"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

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
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM)
		defer cancel()
		ready := make(chan struct{})
		registered := make(chan struct{}, 1)
		cliproxy.SetGlobalModelRegistryHook(proxyModelHook{registered})
		defer cliproxy.SetGlobalModelRegistryHook(nil)
		go func() {
			limit := time.NewTimer(5 * time.Second)
			defer limit.Stop()
			settle := time.NewTimer(time.Hour)
			settle.Stop()
			defer settle.Stop()
			var quiet <-chan time.Time
			for {
				select {
				case <-registered:
					settle.Reset(200 * time.Millisecond)
					quiet = settle.C
				case <-quiet:
					close(ready)
					return
				case <-limit.C:
					close(ready)
					return
				case <-ctx.Done():
					return
				}
			}
		}()
		gated := &readyListener{Listener: listener, ready: ready, done: ctx.Done()}
		service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).WithListener(gated).Build()
		if err != nil {
			return err
		}
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
		return service.Run(ctx)
	}}
	cmd.Flags().StringVar(&path, "config", "", "Proxy config path")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

type readyListener struct {
	net.Listener
	ready <-chan struct{}
	done  <-chan struct{}
}

func (l *readyListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case <-l.ready:
		return l.Listener.Accept()
	}
}

type proxyModelHook struct{ registered chan<- struct{} }

func (h proxyModelHook) OnModelsRegistered(_ context.Context, _, _ string, models []*cliproxy.ModelInfo) {
	if len(models) > 0 {
		select {
		case h.registered <- struct{}{}:
		default:
		}
	}
}
func (proxyModelHook) OnModelsUnregistered(context.Context, string, string) {}
