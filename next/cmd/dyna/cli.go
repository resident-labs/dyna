package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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

	op := &cobra.Command{Use: "op"}
	opList := &cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().OpList(cmd.Context(), contract.OpListRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(r.Operations)
	}}
	opApprove := &cobra.Command{Use: "approve ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().OpDecide(cmd.Context(), contract.OpDecideRequest{ID: contract.OpID(args[0]), Approve: true})
		return err
	}}
	var denyReason string
	opDeny := &cobra.Command{Use: "deny ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().OpDecide(cmd.Context(), contract.OpDecideRequest{ID: contract.OpID(args[0]), Approve: false, Reason: denyReason})
		return err
	}}
	opDeny.Flags().StringVar(&denyReason, "reason", "", "Why the operation is denied")
	_ = opDeny.MarkFlagRequired("reason") // fails only for an undefined flag, and "reason" is defined above
	op.AddCommand(&cobra.Command{Use: "withdraw ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().OpWithdraw(cmd.Context(), contract.OpWithdrawRequest{ID: contract.OpID(args[0])})
		return err
	}})
	op.AddCommand(opList, opApprove, opDeny)
	root.AddCommand(op)

	connect := &cobra.Command{Use: "connect"}
	type connectClient interface {
		ConnectStart(context.Context, contract.ConnectStartRequest) (contract.ConnectStartResponse, error)
		ConnectStatus(context.Context, contract.ConnectStatusRequest) (contract.ConnectStatusResponse, error)
		ConnectCancel(context.Context, contract.ConnectCancelRequest) (contract.ConnectCancelResponse, error)
		ConnectUnlink(context.Context, contract.ConnectUnlinkRequest) (contract.ConnectUnlinkResponse, error)
	}
	getConnect := func() (connectClient, error) {
		c, ok := getClient().(connectClient)
		if !ok {
			return nil, fmt.Errorf("Connect unavailable")
		}
		return c, nil
	}
	var connectRelay, connectName string
	connectStart := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectStart(cmd.Context(), contract.ConnectStartRequest{Relay: connectRelay, Name: connectName})
		return err
	}}
	connectStart.Flags().StringVar(&connectRelay, "relay", "", "Relay origin")
	connectStart.Flags().StringVar(&connectName, "name", "", "Machine name")
	connect.AddCommand(connectStart, &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		status, err := c.ConnectStatus(cmd.Context(), contract.ConnectStatusRequest{})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}, &cobra.Command{Use: "cancel", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectCancel(cmd.Context(), contract.ConnectCancelRequest{})
		return err
	}}, &cobra.Command{Use: "unlink", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectUnlink(cmd.Context(), contract.ConnectUnlinkRequest{})
		return err
	}})
	root.AddCommand(connect)

	device := &cobra.Command{Use: "device"}
	var pairScopes []string
	devicePair := &cobra.Command{Use: "pair", Args: cobra.NoArgs, Short: "Make a pairing code", Long: "Make a 10-character pairing code. It works once, lives 5 minutes, and dies after 5 wrong tries. A new code replaces the old one. Without --scope, the code grants every scope on a loopback route, and read and operate elsewhere.", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().DevicePair(cmd.Context(), contract.DevicePairRequest{Scopes: scopeList(pairScopes)})
		if err == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s expires %s\n", r.Code, r.ExpiresAt.Format(time.RFC3339))
		}
		return err
	}}
	devicePair.Flags().StringArrayVar(&pairScopes, "scope", nil, "Scope to grant (repeatable)")
	deviceList := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().DeviceList(cmd.Context(), contract.DeviceListRequest{})
		if err != nil {
			return err
		}
		for _, d := range r.Devices {
			scopes := make([]string, 0, len(d.Scopes))
			for _, scope := range d.Scopes {
				scopes = append(scopes, string(scope))
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, strings.Join(scopes, ","), d.LastSeenAt.Format(time.RFC3339), d.ExpiresAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	}}
	deviceRevoke := &cobra.Command{Use: "revoke ID", Args: cobra.ExactArgs(1), Short: "Revoke a paired device", Long: "Revoke a paired device. It fails on its next request, and an open stream or terminal ends within 15 seconds.", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceRevoke(cmd.Context(), contract.DeviceRevokeRequest{ID: args[0]})
		return err
	}}
	var setScopes []string
	deviceSetScopes := &cobra.Command{Use: "set-scopes ID", Args: cobra.ExactArgs(1), Short: "Replace the scopes of a paired device", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceSetScopes(cmd.Context(), contract.DeviceSetScopesRequest{ID: args[0], Scopes: scopeList(setScopes)})
		return err
	}}
	deviceSetScopes.Flags().StringArrayVar(&setScopes, "scope", nil, "Scope to grant (repeatable)")
	_ = deviceSetScopes.MarkFlagRequired("scope") // fails only for an undefined flag, and "scope" is defined above
	var setHostName string
	deviceSet := &cobra.Command{Use: "set ID --host HOST", Args: cobra.ExactArgs(1), Short: "Name the host a paired device is on", Long: "Name the host a paired device is on, as in --host omarchy, so the owner's inputs from that device say which machine they come from. --host '' clears it.", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceSetHost(cmd.Context(), contract.DeviceSetHostRequest{ID: args[0], Host: contract.HostName(setHostName)})
		return err
	}}
	deviceSet.Flags().StringVar(&setHostName, "host", "", "Host the device is on (empty clears it)")
	_ = deviceSet.MarkFlagRequired("host") // fails only for an undefined flag
	device.AddCommand(devicePair, deviceList, deviceRevoke, deviceSetScopes, deviceSet)
	root.AddCommand(device)

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
	root.AddCommand(pr)
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
