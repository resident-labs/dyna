package main

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func agyCommand(getClient func() client) *cobra.Command {
	agy := &cobra.Command{Use: "agy"}
	var replace bool
	seed := &cobra.Command{Use: "seed HOST", Short: "Copy the signed-in agy account to a host", Long: "Copy the signed-in agy account folder, and the program if the host has none, to HOST from the host the owner signed in on, so agy sessions can run there. The files travel as they are; Dyna never reads or logs them. A folder already on HOST stays unless --replace is given, which a re-login on the source needs. Refused while a session on an agy profile runs on HOST, and for an exec-only host or one with agents off. The host list marks a host as agy once its next probe finds both.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().HostAgySeed(cmd.Context(), contract.HostAgySeedRequest{Host: contract.HostName(args[0]), Replace: replace})
		return err
	}}
	seed.Flags().BoolVar(&replace, "replace", false, "Replace the account folder already on the host")
	agy.AddCommand(seed)
	return agy
}
