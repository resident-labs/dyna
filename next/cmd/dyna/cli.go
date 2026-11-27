package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e) }

func defaultSocket() string {
	if socket := os.Getenv("DYNA_SOCKET"); socket != "" {
		return socket
	}
	const boxSocket = "/run/dyna/engine/engine.sock"
	if _, err := os.Stat(boxSocket); err == nil {
		return boxSocket
	}
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "dyna", "owner.sock")
}

func addCLI(root *cobra.Command, getClient func() client) {
	hostexec, run := hostexecCommands(getClient)
	root.AddCommand(hostexec, run)

	root.AddCommand(hostCommand(getClient))
	root.AddCommand(agyCommand(getClient))
	root.AddCommand(poolCommand(getClient))
	root.AddCommand(serviceCommand(getClient))
	root.AddCommand(browserCommand(getClient))
	root.AddCommand(profileCommand(getClient), accountCommand(getClient), addonCommand(getClient))
	root.AddCommand(discordCommand(getClient))

	root.AddCommand(roomCommand(getClient))
	owner := &cobra.Command{Use: "owner"}
	owner.AddCommand(&cobra.Command{Use: "timezone [ZONE]", Short: "Read or set the owner's IANA time zone; empty clears it", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.OwnerTimezoneRequest{}
		if len(args) == 1 {
			req.Zone = &args[0]
		}
		result, err := getClient().OwnerTimezone(cmd.Context(), req)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}})
	root.AddCommand(owner)

	root.AddCommand(sessionCommand(getClient))
	root.AddCommand(postCommand(getClient), visualCommand())
	root.AddCommand(goalCommand(getClient))
	root.AddCommand(contextCommand(getClient))

	root.AddCommand(opCommand(getClient))

	root.AddCommand(connectCommand(getClient))

	root.AddCommand(deviceCommand(getClient))

	root.AddCommand(prCommand(getClient))
	root.AddCommand(issueCommand(getClient))
}

// printCall calls method and prints its answer as JSON.
func printCall[R, Q any](cmd *cobra.Command, c *socketClient, method contract.Method, input Q) error {
	result, err := call[Q, R](cmd.Context(), c, method, input)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}
