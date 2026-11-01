// Command dyna is the one binary for the engine, the box agent and the CLI.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	"strings"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var exit commandExit
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
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
			return agent.Run(ctx)
		},
	})
	var socket string
	root.PersistentFlags().StringVar(&socket, "socket", defaultSocket(), "Engine socket")
	addCLI(root, func() client { return newClient(socket) })
	web := &cobra.Command{Use: "web", Long: "If the engine runs on another machine, forward its port first, for example ssh -L 7790:127.0.0.1:7790 <user>@<host>. Run dyna web on that machine and open the printed URL here.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := call[contract.WebLoginRequest, contract.WebLoginResponse](cmd.Context(), newClient(socket), contract.MethodWebLogin, contract.WebLoginRequest{})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), r.URL)
		return err
	}}
	web.AddCommand(webSearchCommand(func() *socketClient { return newClient(socket) }))
	root.AddCommand(web, websearchKeyCommand(func() *socketClient { return newClient(socket) }), questionCommand(func() *socketClient { return newClient(socket) }), subscriptionCommand(func() *socketClient { return newClient(socket) }), workflowCommand(func() *socketClient { return newClient(socket) }))
	// LEGACY(v1-transfer): registers dyna import. Remove after the transfer.
	root.AddCommand(importCommand())
	root.AddCommand(computerCommand("/run/dyna/agent.sock"))
	root.AddCommand(proxyCommand(func() *socketClient { return newClient(socket) }))
	root.AddCommand(ciEgressCommand())
	addEngine(root)
	for _, a := range tools.Actions() {
		for _, binding := range a.CLI {
			cmd, rest, err := root.Find(strings.Fields(binding))
			if err == nil && len(rest) == 0 {
				cmd.Short = a.Help
			}
		}
	}
	for _, b := range tools.BoxActions() {
		for _, binding := range b.CLI {
			cmd, rest, err := root.Find(strings.Fields(binding))
			if err == nil && len(rest) == 0 {
				cmd.Short = b.Help
			}
		}
	}
	return root
}
