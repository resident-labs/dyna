package main

import (
	"encoding/json"
	"os"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func workflowCommand(get func() *socketClient) *cobra.Command {
	c := &cobra.Command{Use: "workflow"}
	var room, args, resume, clientID string
	run := &cobra.Command{Use: "run FILE", Short: "Start a workflow script. Its result arrives as a final reply.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, files []string) error {
		script, err := os.ReadFile(files[0])
		if err != nil {
			return err
		}
		result, err := call[contract.WorkflowRunRequest, contract.WorkflowRunResponse](cmd.Context(), get(), contract.MethodWorkflowRun, contract.WorkflowRunRequest{Room: contract.RoomID(room), Script: string(script), Args: args, Resume: contract.WorkflowRunID(resume), ClientID: clientID})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	run.Flags().StringVar(&room, "room", "", "Room; a session may omit it for its own")
	run.Flags().StringVar(&args, "args", "", "JSON the script reads as args")
	run.Flags().StringVar(&resume, "resume", "", "An ended run whose finished steps this run keeps, so only changed steps run; reload changes a running one")
	run.Flags().StringVar(&clientID, "client-id", "", "Retry key: the same key returns the first run")
	reload := &cobra.Command{Use: "reload RUN FILE", Short: "Give a running run a new script, in the same crew and box. Finished steps return their results, running ones are joined, and new ones start.", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		script, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		result, err := call[contract.WorkflowReloadRequest, contract.WorkflowReloadResponse](cmd.Context(), get(), contract.MethodWorkflowReload, contract.WorkflowReloadRequest{Run: contract.WorkflowRunID(args[0]), Script: string(script)})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	status := &cobra.Command{Use: "status RUN", Short: "Show one run and its steps", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, runs []string) error {
		result, err := call[contract.WorkflowStatusRequest, contract.WorkflowStatusResponse](cmd.Context(), get(), contract.MethodWorkflowStatus, contract.WorkflowStatusRequest{Run: contract.WorkflowRunID(runs[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	list := &cobra.Command{Use: "list", Short: "List a room's workflow runs, newest first", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.WorkflowListRequest, contract.WorkflowListResponse](cmd.Context(), get(), contract.MethodWorkflowList, contract.WorkflowListRequest{Room: contract.RoomID(room)})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	list.Flags().StringVar(&room, "room", "", "Room; a session may omit it for its own")
	c.AddCommand(run, reload, status, list)
	return c
}
