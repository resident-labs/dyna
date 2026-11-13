package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// issueCommand holds the issue commands, which mirror pr list, status, track and untrack.
func issueCommand(getClient func() client) *cobra.Command {
	issue := &cobra.Command{Use: "issue"}
	var repo, room, session string
	var all bool
	number := func(arg string) (int, error) {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%q is not an issue number", arg)
		}
		return n, nil
	}
	printIssue := func(cmd *cobra.Command, i contract.IssueSummary) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s#%d\t%s\tlabels=%s\tassignees=%s\t%s\n", i.Repo, i.Number, i.State, strings.Join(i.Labels, ","), strings.Join(i.Assignees, ","), i.URL)
		return err
	}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List tracked issues", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().IssueList(cmd.Context(), contract.IssueListRequest{Room: contract.RoomID(room), All: all})
		if err != nil {
			return err
		}
		for _, i := range r.Issues {
			if err := printIssue(cmd, i); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().StringVar(&room, "room", "", "Room id (default: the session's room, or every room for the owner)")
	list.Flags().BoolVar(&all, "all", false, "Also list closed issues")
	status := &cobra.Command{Use: "status NUMBER", Args: cobra.ExactArgs(1), Short: "Show a tracked issue as of the last change told", RunE: func(cmd *cobra.Command, args []string) error {
		n, err := number(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().IssueStatus(cmd.Context(), contract.IssueStatusRequest{Repo: contract.RepoName(repo), Number: n})
		if err != nil {
			return err
		}
		return printIssue(cmd, r.Issue)
	}}
	track := &cobra.Command{Use: "track NUMBER", Args: cobra.ExactArgs(1), Short: "Track an issue and wake its session when it changes", RunE: func(cmd *cobra.Command, args []string) error {
		n, err := number(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().IssueTrack(cmd.Context(), contract.IssueTrackRequest{Repo: contract.RepoName(repo), Number: n, Room: contract.RoomID(room), Session: contract.SessionID(session)})
		if err != nil {
			return err
		}
		return printIssue(cmd, r.Issue)
	}}
	track.Flags().StringVar(&room, "room", "", "Owner only: room whose root owns the issue")
	track.Flags().StringVar(&session, "session", "", "Owner only: session id that owns the issue")
	untrack := &cobra.Command{Use: "untrack NUMBER", Args: cobra.ExactArgs(1), Short: "Stop tracking an issue", RunE: func(cmd *cobra.Command, args []string) error {
		n, err := number(args[0])
		if err != nil {
			return err
		}
		_, err = getClient().IssueUntrack(cmd.Context(), contract.IssueUntrackRequest{Repo: contract.RepoName(repo), Number: n})
		return err
	}}
	for _, command := range []*cobra.Command{status, track, untrack} {
		command.Flags().StringVar(&repo, "repo", "", "Repository as owner/name (default: the session's repository)")
	}
	issue.AddCommand(list, status, track, untrack)
	return issue
}
