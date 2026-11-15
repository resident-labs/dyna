package main

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func advisorCommand(get func() *socketClient) *cobra.Command {
	return &cobra.Command{Use: "advisor PROMPT", Short: "Ask the room's advisor, as root, and print its answer", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return printCall[contract.AdvisorResponse](cmd, get(), contract.MethodAdvisor, contract.AdvisorRequest{Prompt: args[0]})
	}}
}
