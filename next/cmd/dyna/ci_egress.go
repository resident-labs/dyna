package main

import (
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/egress"
	"github.com/spf13/cobra"
)

// ciEgressCommand is the proxy of a CI host. It runs in a container the pool starts.
func ciEgressCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{Use: "ci-egress", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var listeners []net.Listener
		for slot := range box.RunnerSlots {
			listener, err := egressSocket(filepath.Join(dir, strconv.Itoa(slot)))
			if err != nil {
				return err
			}
			listeners = append(listeners, listener)
		}
		return egress.Serve(ctx, listeners, egress.PublicDial())
	}}
	cmd.Flags().StringVar(&dir, "dir", "", "Directory for one subdirectory and socket per runner slot")
	_ = cmd.MarkFlagRequired("dir") // fails only for an undefined flag
	return cmd
}

// egressSocket makes one slot's socket. A runner mounts only its own slot's
// directory, so that directory must outlive a restart of the proxy.
func egressSocket(dir string) (net.Listener, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, box.EgressSocket)
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", socket)
}
