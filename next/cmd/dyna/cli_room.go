package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func roomCommand(getClient func() client) *cobra.Command {
	room := &cobra.Command{Use: "room"}
	room.AddCommand(roomGitHubCommand(getClient))
	room.AddCommand(roomDiscordCommand(getClient))
	for _, paused := range []bool{true, false} {
		name := "resume"
		if paused {
			name = "pause"
		}
		room.AddCommand(&cobra.Command{Use: name + " ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, err := getClient().RoomPause(cmd.Context(), contract.RoomPauseRequest{Room: contract.RoomID(args[0]), Paused: paused})
			return err
		}})
	}
	var limitCPUs int
	var limitMem string
	limit := &cobra.Command{Use: "limit ROOM --cpus N --mem SIZE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		mem, err := humanize.ParseBytes(limitMem)
		if err != nil || mem > math.MaxInt64 {
			return fmt.Errorf("invalid memory size %q", limitMem)
		}
		_, err = getClient().RoomSetLimits(cmd.Context(), contract.RoomSetLimitsRequest{Room: contract.RoomID(args[0]), CPUs: limitCPUs, MemBytes: int64(mem)})
		return err
	}}
	limit.Flags().IntVar(&limitCPUs, "cpus", 0, "CPU limit; 0 means unlimited")
	limit.Flags().StringVar(&limitMem, "mem", "", "Memory limit; 0 means unlimited")
	_ = limit.MarkFlagRequired("cpus")
	_ = limit.MarkFlagRequired("mem")
	var githubWrites string
	rootApproves := map[contract.RootApproval]*string{}
	for _, kind := range contract.AllRootApprovals {
		rootApproves[kind] = new(string)
	}
	setRoom := &cobra.Command{Use: "set ROOM [--root-approves-KIND on|off] [--github-writes GROUPS@REPOS|off]", Short: "Change a room setting", Long: "Change a room setting; a setting left out stays. --github-writes lets the room's sessions make GitHub writes without a host grant: GROUPS are issues, pulls, projects and actions, comma-separated, and REPOS are repositories of the room or *, comma-separated, as in issues,projects@owner/repo; off turns it off. It is off by default, and merges, deletes and everything else stay refused. With --root-approves-KIND on, the room's root decides that kind of its sessions' requests with op approve and op deny, as the owner can, and the owner still can. KIND is addons (global skills, packages and MCP servers), hosts (room hosts rules) or pools (pool add). Each is on by default, and the request still waits for a decision.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.RoomSetRequest{Room: contract.RoomID(args[0]), RootApproves: map[contract.RootApproval]bool{}}
		for _, kind := range contract.AllRootApprovals {
			if *rootApproves[kind] == "" {
				continue
			}
			on, err := hostSwitch("root-approves-"+string(kind), *rootApproves[kind])
			if err != nil {
				return err
			}
			req.RootApproves[kind] = *on
		}
		if githubWrites != "" {
			writes, err := parseGitHubWrites(githubWrites)
			if err != nil {
				return err
			}
			req.GitHubWrites = &writes
		}
		if len(req.RootApproves) == 0 && req.GitHubWrites == nil {
			return fmt.Errorf("set at least one --root-approves-KIND on|off or --github-writes")
		}
		_, err := getClient().RoomSet(cmd.Context(), req)
		return err
	}}
	for _, kind := range contract.AllRootApprovals {
		setRoom.Flags().StringVar(rootApproves[kind], "root-approves-"+string(kind), "", "on or off: whether the room's root may decide its sessions' "+string(kind)+" requests")
	}
	setRoom.Flags().StringVar(&githubWrites, "github-writes", "", "GROUPS@REPOS (issues,pulls,projects @ repositories of the room or *), or off: the GitHub writes the room's sessions may make")
	room.AddCommand(limit, setRoom, roomHostsCommand(getClient))
	var rootProfile string
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().RoomCreate(cmd.Context(), contract.RoomCreateRequest{Name: args[0], RootProfile: contract.ProfileName(rootProfile)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Room)
		}
		return err
	}}
	create.Flags().StringVar(&rootProfile, "root-profile", "", "Root session profile")
	var discardRoom bool
	deleteRoom := &cobra.Command{Use: "delete ROOM --discard", Short: "Delete a room and all its data", Long: "Delete a room: its sessions, boxes, posts, files, transcripts and shared folder. Unpushed work is lost and archive refs are deleted. The deletion runs in the background; it resumes after a restart. A host that is offline is cleaned when it returns; host list counts what it still holds. It refuses while an account or host is limited to this room alone, because an empty limit means every room: change those limits first.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !discardRoom {
			return fmt.Errorf("deleting a room discards all its work; pass --discard to confirm")
		}
		_, err := getClient().RoomDelete(cmd.Context(), contract.RoomDeleteRequest{Room: contract.RoomID(args[0]), Discard: true})
		return err
	}}
	deleteRoom.Flags().BoolVar(&discardRoom, "discard", false, "Confirm that all the room's work is discarded")
	var follow bool
	log := &cobra.Command{Use: "log ROOM", Short: "Read room events; list event types with dyna subscription events", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		room := contract.RoomID(args[0])
		var after int64
		printEvent := func(e contract.Event) error {
			if e.Seq == 0 {
				return nil
			}
			data, err := json.Marshal(e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}
		for {
			r, err := c.EventsList(cmd.Context(), contract.EventsListRequest{Room: room, AfterSeq: after, Limit: 256})
			if err != nil {
				return err
			}
			for _, e := range r.Events {
				if err := printEvent(e); err != nil {
					return err
				}
				after = e.Seq
			}
			if len(r.Events) < 256 {
				break
			}
		}
		if follow {
			return c.Events(cmd.Context(), room, after, printEvent)
		}
		return nil
	}}
	log.Flags().BoolVar(&follow, "follow", false, "Follow new events")
	repo := &cobra.Command{Use: "repo"}
	var makeDefault bool
	var prEvents string
	addRepo := &cobra.Command{Use: "add ROOM REPO", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if prEvents != "on" && prEvents != "off" {
			return fmt.Errorf("--pr-events is on or off")
		}
		_, err := getClient().RoomRepoAdd(cmd.Context(), contract.RoomRepoAddRequest{Room: contract.RoomID(args[0]), Repo: contract.RepoName(args[1]), Default: makeDefault, PREvents: prEvents == "on"})
		return err
	}}
	addRepo.Flags().BoolVar(&makeDefault, "default", false, "Make this the room default")
	addRepo.Flags().StringVar(&prEvents, "pr-events", "off", "Room-wide PR events: on or off (initial census is silent)")
	listRepos := &cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().RoomRepos(cmd.Context(), contract.RoomReposRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		for _, repo := range r.Repos {
			if repo.Default {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s (default)\n", repo.Repo); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(cmd.OutOrStdout(), repo.Repo); err != nil {
				return err
			}
		}
		return nil
	}}
	var ciOn, ciOff bool
	var ciMax int
	repoCI := &cobra.Command{Use: "ci REPO", Short: "Turn a repository's CI runners on or off", Long: "--on serves the repository's jobs that say runs-on: dyna-small, dyna-medium, dyna-large or dyna-xlarge, each from a runner of that size, on a host with ci on that admission picks as it picks one for a box, or on a node of the room's pool. --max bounds its runners across sizes. --off removes them at once. See next/docs/ci-runners.md.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().RepoCISet(cmd.Context(), contract.RepoCISetRequest{Repo: contract.RepoName(args[0]), On: ciOn, Max: ciMax})
		return err
	}}
	repoCI.Flags().BoolVar(&ciOn, "on", false, "Serve the repository's jobs from Dyna's runners")
	repoCI.Flags().BoolVar(&ciOff, "off", false, "Stop its runners, even mid-job, and leave its jobs to GitHub-hosted runners")
	repoCI.Flags().IntVar(&ciMax, "max", 0, "Most runners at once across sizes; none means as many as admission allows")
	repoCI.MarkFlagsOneRequired("on", "off")
	repoCI.MarkFlagsMutuallyExclusive("on", "off")
	repoCI.MarkFlagsMutuallyExclusive("off", "max")
	repo.AddCommand(addRepo, listRepos, repoCI)
	room.AddCommand(create, deleteRoom, log, repo)
	return room
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
