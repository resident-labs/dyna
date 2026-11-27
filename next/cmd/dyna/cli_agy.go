package main

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func agyCommand(getClient func() client) *cobra.Command {
	agy := &cobra.Command{Use: "agy"}
	seed := &cobra.Command{Use: "seed HOST", Short: "Copy the signed-in agy account to a host", Long: "Copy the signed-in agy account folder, and the program if the host has none, to HOST from the host the owner signed in on, so agy sessions can run there. The files travel as they are; Dyna never reads or logs them. A host that already has an account folder is refused, and a folder is never replaced in place: to re-seed, close its agy sessions and every agy process using the old folder, remove the folder on that host, and seed again. Refused for the source, an unknown host, an exec-only host and one with agents off. The copy leaves out what agy writes per session (conversations, brain, logs, annotations) and is a few MB, but it can take minutes on a slow link: the command waits and has no timeout of its own, so do not wrap it in a short one. If a running agy session changes a file while it is packed, the seed fails with a retry message and the host is left unchanged. The host list marks a host as agy once its next probe finds both.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().HostAgySeed(cmd.Context(), contract.HostAgySeedRequest{Host: contract.HostName(args[0])})
		return err
	}}
	agy.AddCommand(seed)
	return agy
}
