package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func instructionsCommand(get func() *socketClient) *cobra.Command {
	c := &cobra.Command{Use: "instructions", Short: "The global and room AGENTS.md files"}
	var room, base string
	read := &cobra.Command{Use: "get", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.InstructionsGetRequest, contract.InstructionsGetResponse](cmd.Context(), get(), contract.MethodInstructionsGet, contract.InstructionsGetRequest{Room: contract.RoomID(room)})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	read.Flags().StringVar(&room, "room", "", "Room; omitted, the global file")
	save := &cobra.Command{Use: "set [FILE]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var text []byte
		var err error
		if len(args) == 1 && args[0] != "-" {
			text, err = os.ReadFile(args[0])
		} else {
			text, err = io.ReadAll(cmd.InOrStdin())
		}
		if err != nil {
			return err
		}
		result, err := call[contract.InstructionsSetRequest, contract.InstructionsSetResponse](cmd.Context(), get(), contract.MethodInstructionsSet, contract.InstructionsSetRequest{Room: contract.RoomID(room), Text: string(text), Base: base})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	save.Flags().StringVar(&room, "room", "", "Room; omitted, the global file")
	save.Flags().StringVar(&base, "base", "", "The hash get returned (required)")
	_ = save.MarkFlagRequired("base")
	c.AddCommand(read, save)
	return c
}
