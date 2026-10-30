// Command dyna is the one binary for the engine, the box agent and the CLI.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "dyna",
		Short:        "Dyna agent harness",
		SilenceUsage: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the Dyna version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	})
	root.AddCommand(&cobra.Command{
		Use: "box-agent", Short: "Run the box agent", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return agent.Run(ctx, version)
		},
	})
	var socket string
	root.PersistentFlags().StringVar(&socket, "socket", defaultSocket(), "Engine socket")
	addCLI(root, func() client { return newClient(socket) })
	root.AddCommand(&cobra.Command{Use: "web", Long: "The engine runs on hermeslab. Reach it with ssh -L 7790:127.0.0.1:7790 pip@hermeslab, run dyna web there over ssh, and open the printed URL in the desktop browser.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := call[contract.WebLoginRequest, contract.WebLoginResponse](cmd.Context(), newClient(socket), contract.MethodWebLogin, contract.WebLoginRequest{})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), r.URL)
		return err
	}})
	root.AddCommand(computerCommand("/run/dyna/agent.sock"))
	addEngine(root)
	return root
}
