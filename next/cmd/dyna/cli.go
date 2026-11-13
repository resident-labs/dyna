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
	root.AddCommand(poolCommand(getClient))
	root.AddCommand(serviceCommand(getClient))
	root.AddCommand(profileCommand(getClient), accountCommand(getClient), addonCommand(getClient))
	root.AddCommand(discordCommand(getClient))

	root.AddCommand(roomCommand(getClient))

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
