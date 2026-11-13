package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func prCommand(getClient func() client) *cobra.Command {
	pr := &cobra.Command{Use: "pr"}
	var prTitle, prBodyFile, prBase, prHead string
	var prDraft bool
	prOpen := &cobra.Command{Use: "open", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var body string
		if prBodyFile != "" {
			data, err := os.ReadFile(prBodyFile)
			if err != nil {
				return err
			}
			body = string(data)
		}
		r, err := getClient().OpSubmit(cmd.Context(), contract.OpSubmitRequest{Operation: contract.Operation{
			Kind: "pr.open",
			Args: contract.PrOpenArgs{Title: prTitle, Body: body, Base: prBase, Head: prHead, Draft: prDraft},
		}})
		if err != nil {
			return err
		}
		return printPrResult(cmd, r.Operation, "pr open")
	}}
	prOpen.Flags().StringVar(&prTitle, "title", "", "Pull request title")
	prOpen.Flags().StringVar(&prBodyFile, "body-file", "", "File with the pull request body")
	prOpen.Flags().StringVar(&prHead, "head", "", "Head branch (default: session branch)")
	prOpen.Flags().StringVar(&prBase, "base", "main", "Base branch")
	prOpen.Flags().BoolVar(&prDraft, "draft", false, "Open as a draft")
	_ = prOpen.MarkFlagRequired("title") // fails only for an undefined flag, and "title" is defined above
	var prRepo, prRoom, prSession string
	var prAll bool
	prNumber := func(arg string) (int, error) {
		number, err := strconv.Atoi(arg)
		if err != nil || number <= 0 {
			return 0, fmt.Errorf("%q is not a pull request number", arg)
		}
		return number, nil
	}
	printPR := func(cmd *cobra.Command, p contract.PrSummary) error {
		stack := ""
		if p.Stack > 0 {
			stack = fmt.Sprintf("\tstack #%d %d/%d", p.Stack, p.Position, p.StackSize)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s#%d\t%s\tci=%s\treview=%s\tmerge=%s\t%s%s\n", p.Repo, p.Number, p.State, p.CI, p.Review, p.Merge, p.URL, stack)
		if err == nil && p.CI == contract.PrCICancelled {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "CI cancelled: "+strings.Join(p.CancelledChecks, ", "))
		}
		return err
	}
	prList := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List tracked pull requests", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().PrList(cmd.Context(), contract.PrListRequest{Room: contract.RoomID(prRoom), All: prAll})
		if err != nil {
			return err
		}
		for _, p := range r.PRs {
			if err := printPR(cmd, p); err != nil {
				return err
			}
		}
		return nil
	}}
	prList.Flags().StringVar(&prRoom, "room", "", "Room id (default: the session's room, or every room for the owner)")
	prList.Flags().BoolVar(&prAll, "all", false, "Also list merged and closed pull requests")
	prStatus := &cobra.Command{Use: "status NUMBER", Args: cobra.ExactArgs(1), Short: "Show a tracked pull request as of the last poll", RunE: func(cmd *cobra.Command, args []string) error {
		number, err := prNumber(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().PrStatus(cmd.Context(), contract.PrStatusRequest{Repo: contract.RepoName(prRepo), Number: number})
		if err != nil {
			return err
		}
		return printPR(cmd, r.PR)
	}}
	prTrack := &cobra.Command{Use: "track NUMBER", Args: cobra.ExactArgs(1), Short: "Track a pull request and wake its session when it changes", RunE: func(cmd *cobra.Command, args []string) error {
		number, err := prNumber(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().PrTrack(cmd.Context(), contract.PrTrackRequest{Repo: contract.RepoName(prRepo), Number: number, Room: contract.RoomID(prRoom), Session: contract.SessionID(prSession)})
		if err != nil {
			return err
		}
		return printPR(cmd, r.PR)
	}}
	prTrack.Flags().StringVar(&prRoom, "room", "", "Owner only: room whose root owns the pull request")
	prTrack.Flags().StringVar(&prSession, "session", "", "Owner only: session id that owns the pull request")
	prUntrack := &cobra.Command{Use: "untrack NUMBER", Args: cobra.ExactArgs(1), Short: "Stop tracking a pull request", RunE: func(cmd *cobra.Command, args []string) error {
		number, err := prNumber(args[0])
		if err != nil {
			return err
		}
		_, err = getClient().PrUntrack(cmd.Context(), contract.PrUntrackRequest{Repo: contract.RepoName(prRepo), Number: number})
		return err
	}}
	for _, command := range []*cobra.Command{prStatus, prTrack, prUntrack} {
		command.Flags().StringVar(&prRepo, "repo", "", "Base repository as owner/name (default: the session's repository)")
	}
	prStack := &cobra.Command{Use: "stack NUMBER NUMBER...", Args: cobra.RangeArgs(2, 20), Short: "Create a GitHub stack, bottom first", RunE: func(cmd *cobra.Command, args []string) error {
		prs := make([]int, len(args))
		for i, arg := range args {
			n, err := prNumber(arg)
			if err != nil {
				return err
			}
			prs[i] = n
		}
		r, err := getClient().OpSubmit(cmd.Context(), contract.OpSubmitRequest{Operation: contract.Operation{Kind: "pr.stack", Args: contract.PrStackArgs{PRs: prs}}})
		if err != nil {
			return err
		}
		return printPrResult(cmd, r.Operation, "pr stack")
	}}
	pr.AddCommand(prOpen, prList, prStatus, prTrack, prUntrack, prStack)
	return pr
}

// printPrResult prints the shared operation result for PR-writing commands.
func printPrResult(cmd *cobra.Command, op contract.Operation, action string) error {
	if op.Result == nil {
		return fmt.Errorf("%s %s has no result yet (state %s)", action, op.ID, op.State)
	}
	if op.Result.Error != "" || op.Result.ExitCode != 0 {
		return fmt.Errorf("%s %s failed (exit code %d): %s", action, op.ID, op.Result.ExitCode, op.Result.Error)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), op.Result.Output)
	return err
}
