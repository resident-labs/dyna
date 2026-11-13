package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e) }

// scopeList turns repeated --scope flags into typed scopes. The engine checks the values.
func scopeList(values []string) []contract.Scope {
	scopes := make([]contract.Scope, 0, len(values))
	for _, value := range values {
		scopes = append(scopes, contract.Scope(value))
	}
	return scopes
}

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

// goalCommand holds the goal commands. Inside a box --room may stay empty: the engine uses the caller's room.
func goalCommand(getClient func() client) *cobra.Command {
	goal := &cobra.Command{Use: "goal", Short: "Work on goals and their checklists"}
	var roomID string
	goal.PersistentFlags().StringVar(&roomID, "room", "", "Room ID")
	printJSON := func(cmd *cobra.Command, value any) error {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	var id, holder, text, doneWhen, clientID string
	set := &cobra.Command{Use: "set", Short: "Create a goal, change one, or hand it to another session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().GoalSet(cmd.Context(), contract.GoalSetRequest{ClientID: clientID, Room: contract.RoomID(roomID), Goal: contract.GoalID(id), Session: contract.SessionID(holder), Text: text, DoneWhen: doneWhen})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Goal)
		}
		return err
	}}
	set.Flags().StringVar(&id, "goal", "", "Goal to change")
	set.Flags().StringVar(&holder, "session", "", "Session that holds it")
	set.Flags().StringVar(&text, "text", "", "What the goal is")
	set.Flags().StringVar(&doneWhen, "done-when", "", "When it is done")
	set.Flags().StringVar(&clientID, "client-id", "", "Retry key")
	var all bool
	var listHolder string
	list := &cobra.Command{Use: "list", Short: "List a room's goals with their items", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().GoalList(cmd.Context(), contract.GoalListRequest{Room: contract.RoomID(roomID), Session: contract.SessionID(listHolder), All: all})
		if err != nil {
			return err
		}
		return printJSON(cmd, r.Goals)
	}}
	list.Flags().StringVar(&listHolder, "session", "", "Only this session's goals")
	list.Flags().BoolVar(&all, "all", false, "Include ended goals")
	var item, parent int
	var itemText, itemState string
	itemCmd := &cobra.Command{Use: "item GOAL", Short: "Add a todo item, a subtask with --parent, or change one with --item", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().GoalItem(cmd.Context(), contract.GoalItemRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0]), Item: item, Parent: parent, Text: itemText, State: contract.GoalItemState(itemState)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Item)
		}
		return err
	}}
	itemCmd.Flags().IntVar(&item, "item", 0, "Item number to change")
	itemCmd.Flags().IntVar(&parent, "parent", 0, "Item number a new item is a subtask of")
	itemCmd.Flags().StringVar(&itemText, "text", "", "Item text")
	itemCmd.Flags().StringVar(&itemState, "state", "", "open, done or dropped")
	var drop bool
	done := &cobra.Command{Use: "done GOAL SUMMARY", Short: "End a goal", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().GoalDone(cmd.Context(), contract.GoalDoneRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0]), Summary: args[1], Drop: drop})
		return err
	}}
	done.Flags().BoolVar(&drop, "drop", false, "Give the goal up instead")
	journal := &cobra.Command{Use: "journal GOAL [TEXT]", Short: "Add to a goal's journal and read its newest 5 lines", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.GoalJournalRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0])}
		if len(args) == 2 {
			req.Text = args[1]
		}
		r, err := getClient().GoalJournal(cmd.Context(), req)
		if err != nil {
			return err
		}
		return printJSON(cmd, r)
	}}
	goal.AddCommand(set, list, itemCmd, done, journal)
	return goal
}

// parseGitHubWrites reads --github-writes: `off`, or GROUPS@REPOS with one @, both comma-separated lists with no empty element. A
// group is a known one and a repository is owner/name or *; the engine checks the repositories against the room's.
func parseGitHubWrites(value string) (contract.GitHubWrites, error) {
	writes := contract.GitHubWrites{Repos: []contract.RepoName{}, Groups: []contract.GitHubWriteGroup{}}
	if value == "off" {
		return writes, nil
	}
	groups, repos, found := strings.Cut(value, "@")
	if !found || strings.Contains(repos, "@") {
		return writes, fmt.Errorf("--github-writes is GROUPS@REPOS with one @, such as issues,projects@owner/repo, or off; %q is not", value)
	}
	for _, group := range strings.Split(groups, ",") {
		if !slices.Contains(contract.AllGitHubWriteGroups, contract.GitHubWriteGroup(group)) {
			return writes, fmt.Errorf("--github-writes group %q is not one of %v", group, contract.AllGitHubWriteGroups)
		}
		writes.Groups = append(writes.Groups, contract.GitHubWriteGroup(group))
	}
	for _, repo := range strings.Split(repos, ",") {
		owner, name, slash := strings.Cut(repo, "/")
		if strings.ContainsAny(repo, " \t\r\n") || repo != contract.GitHubWriteAllRepos && (!slash || owner == "" || name == "" || strings.Contains(name, "/")) {
			return writes, fmt.Errorf("--github-writes repository %q is not owner/name or *", repo)
		}
		writes.Repos = append(writes.Repos, contract.RepoName(repo))
	}
	return writes, nil
}

// printCall calls method and prints its answer as JSON.
func printCall[R, Q any](cmd *cobra.Command, c *socketClient, method contract.Method, input Q) error {
	result, err := call[Q, R](cmd.Context(), c, method, input)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}
