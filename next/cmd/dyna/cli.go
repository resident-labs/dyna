package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	dynaroom "github.com/resident-labs/dyna/room"
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

// hostSwitch reads a host switch flag, which is on or off.
func hostSwitch(name, value string) (*bool, error) {
	switch value {
	case "on":
		return new(true), nil
	case "off":
		return new(false), nil
	}
	return nil, fmt.Errorf("--%s takes on or off, not %q", name, value)
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func addCLI(root *cobra.Command, getClient func() client) {
	hostexec := &cobra.Command{Use: "hostexec"}
	var runHost, cwd string
	var timeout time.Duration
	run := &cobra.Command{Use: "bash --host HOST COMMAND", Short: "Run a command on a host with bash -c, under a host grant.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		var id [16]byte
		_, _ = rand.Read(id[:])
		req := contract.HostexecRunRequest{Host: contract.HostName(runHost), Command: args[0], Cwd: cwd, Timeout: contract.DurationMS(timeout.Milliseconds()), ClientID: hex.EncodeToString(id[:])}
		// An interrupt stops the run and still waits for its result. A dropped call would not stop it, so the stop is asked
		// for by name, and a second interrupt ends this at once.
		interrupted, restore := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer restore()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-finished:
				return
			case <-interrupted.Done():
			}
			restore()
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 5*time.Second)
			defer cancel()
			if _, err := c.HostexecStop(stopCtx, contract.HostexecStopRequest{ClientID: req.ClientID}); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "The run could not be stopped:", err)
			}
		}()
		res, err := c.HostexecRun(cmd.Context(), req)
		if err != nil {
			return err
		}
		op := res.Operation
		if op.Result == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Command %s waits for approval; op.ran wakes the session with the result.\n", op.ID)
			return err
		}
		if _, err = io.WriteString(cmd.OutOrStdout(), op.Result.Output); err != nil {
			return err
		}
		if op.Result.OutputFile != "" {
			if err = c.File(cmd.Context(), op.Result.OutputFile, cmd.OutOrStdout()); err != nil {
				return err
			}
		}
		if op.Result.Truncated {
			fmt.Fprintln(cmd.ErrOrStderr(), "Output truncated at 8 MiB.")
		}
		if op.Result.Error != "" {
			return fmt.Errorf("%s", op.Result.Error)
		}
		if op.Result.ExitCode != 0 {
			cmd.SilenceErrors = true
			return commandExit(op.Result.ExitCode)
		}
		return nil
	}}
	var grantHost, grantRoom, grantTo, grantReason string
	grant := &cobra.Command{Use: "grant --host HOST --reason WHY", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		args := contract.HostexecGrantArgs{Room: contract.RoomID(grantRoom), Hosts: []contract.HostName{contract.HostName(grantHost)}, Reason: grantReason}
		if grantTo != "" {
			args.To = contract.Principal{Kind: contract.PrincipalSession, Session: contract.SessionID(grantTo)}
		}
		r, err := getClient().OpSubmit(cmd.Context(), contract.OpSubmitRequest{Operation: contract.Operation{Kind: args.OpKind(), Room: args.Room, Args: args}})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Operation.ID)
		}
		return err
	}}
	grant.Flags().StringVar(&grantHost, "host", "", "Host name")
	grant.Flags().StringVar(&grantReason, "reason", "", "Why the session needs access")
	grant.Flags().StringVar(&grantTo, "to", "", "Session that gets the grant (owner only)")
	grant.Flags().StringVar(&grantRoom, "room", "", "Room ID (owner only)")
	_ = grant.MarkFlagRequired("host")   // fails only for an undefined flag
	_ = grant.MarkFlagRequired("reason") // fails only for an undefined flag
	run.Flags().StringVar(&runHost, "host", "", "Host name")
	run.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	run.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Run timeout")
	_ = run.MarkFlagRequired("host")
	hostexec.AddCommand(grant)
	root.AddCommand(hostexec, run)

	host := &cobra.Command{Use: "host"}
	var ssh string
	var hostRooms []string
	var admin bool
	var addAgents, addCI string
	add := &cobra.Command{Use: "add NAME", Long: "Add a host. With --agents off and --ci off the host is exec only: it passes the base host checks, takes host-exec runs, and never has a box or runner. With --agents on (the default) new session boxes may land on it; with --ci on (the default, except on the host the engine runs on) the CI pool may start runners there, placed as boxes are.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		rooms := make([]contract.RoomID, len(hostRooms))
		for i, room := range hostRooms {
			rooms[i] = contract.RoomID(room)
		}
		agents, err := hostSwitch("agents", addAgents)
		if err != nil {
			return err
		}
		var ci *bool
		if cmd.Flags().Changed("ci") {
			if ci, err = hostSwitch("ci", addCI); err != nil {
				return err
			}
		}
		result, err := getClient().HostAdd(cmd.Context(), contract.HostAddRequest{Name: contract.HostName(args[0]), SSH: ssh, OwnerOnlyAdmin: admin, Rooms: rooms, Agents: agents, CI: ci})
		if err == nil && result.NoSwap {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: host has no swap; memory pressure can kill box processes.")
		}
		return err
	}}
	add.Flags().StringSliceVar(&hostRooms, "room", nil, "Room ID allowed on this host (repeatable; default: all rooms)")
	add.Flags().StringVar(&ssh, "ssh", "", "SSH destination")
	add.Flags().BoolVar(&admin, "owner-only-admin", false, "Confirm that only the owner administers the machine; a host that runs boxes or CI runners needs it, an exec-only host (--agents off --ci off) does not")
	add.Flags().StringVar(&addAgents, "agents", "on", "on or off: whether new session boxes may land on this host")
	add.Flags().StringVar(&addCI, "ci", "", "on or off: whether the CI pool may start runners on this host (default on, off on the engine's own host)")
	var setAgents, setCI string
	var setScale int
	var setAdmin bool
	var clearCreateProblem bool
	setHost := &cobra.Command{Use: "set NAME [--agents on|off] [--ci on|off] [--ci-scale 1-4]", Short: "Turn a host's agents and CI switches", Long: "Turn a host's two switches. With agents off no new session box lands on the host; its room list still says which rooms it serves while agents are on. With ci off the CI pool starts no new runner there, and runners come back when ci is on again. Both off disables the host and keeps it registered. A host added exec only (`host add --agents off --ci off`) passes the base host checks, without box or CI setup. Turning agents or ci on for an exec-only host runs the full host check first, which needs --owner-only-admin as host add does. Boxes and runners already on the host stay until they stop, and a stopped box there waits until agents are on again.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.HostSetRequest{Host: contract.HostName(args[0]), ClearCreateProblem: clearCreateProblem, OwnerOnlyAdmin: setAdmin}
		var err error
		if cmd.Flags().Changed("agents") {
			if req.Agents, err = hostSwitch("agents", setAgents); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("ci") {
			if req.CI, err = hostSwitch("ci", setCI); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("ci-scale") {
			req.CIScale = &setScale
		}
		if req.Agents == nil && req.CI == nil && req.CIScale == nil && !req.ClearCreateProblem {
			return fmt.Errorf("pass --agents, --ci, --ci-scale or --clear-create-problem")
		}
		_, err = getClient().HostSet(cmd.Context(), req)
		return err
	}}
	setHost.Flags().IntVar(&setScale, "ci-scale", 1, "CI reservation capacity multiplier, 1–4 (default 1); real memory guard and 16-runner cap still apply")
	setHost.Flags().BoolVar(&setAdmin, "owner-only-admin", false, "Confirm that only the owner administers the machine; turning agents or ci on for an exec-only host needs it")
	setHost.Flags().BoolVar(&clearCreateProblem, "clear-create-problem", false, "Clear the host's create-failure quarantine")
	setHost.Flags().StringVar(&setAgents, "agents", "", "on or off: whether new session boxes may land on this host")
	setHost.Flags().StringVar(&setCI, "ci", "", "on or off: whether the CI pool may start runners on this host")
	var limitRooms []string
	var limitAll bool
	limitHost := &cobra.Command{Use: "limit NAME", Short: "Set the rooms a host serves", Long: "Replace the rooms a host serves. Boxes already on the host stay there. A room delete refuses while it is the only room of a host, so this is how to clear that refusal.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if limitAll == (len(limitRooms) > 0) {
			return fmt.Errorf("pass --room for each room to serve, or --all for every room")
		}
		rooms := make([]contract.RoomID, len(limitRooms))
		for i, room := range limitRooms {
			rooms[i] = contract.RoomID(room)
		}
		_, err := getClient().HostSetLimits(cmd.Context(), contract.HostSetLimitsRequest{Host: contract.HostName(args[0]), Rooms: rooms})
		return err
	}}
	limitHost.Flags().StringSliceVar(&limitRooms, "room", nil, "Room ID this host serves (repeatable)")
	limitHost.Flags().BoolVar(&limitAll, "all", false, "Serve every room")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "Show each host's disk use, switches, CI scale and runners", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().HostList(cmd.Context(), contract.HostListRequest{})
		if err != nil {
			return err
		}
		// The control plane is the engine's own machine, which is usually also an added host; it places no boxes or runners.
		engine := ""
		for _, h := range r.Hosts {
			if h.EngineHost {
				engine = string(h.Name)
			}
		}
		for _, h := range r.Hosts {
			state, used, total := "ok", humanize.IBytes(uint64(h.DiskUsed)), humanize.IBytes(uint64(h.DiskBytes))
			if h.ExecOnly {
				state, used, total = "offline", "-", "-"
				if h.Reachable {
					state = "online"
				}
			} else if h.DiskLow {
				state = "low"
			}
			if h.Health == contract.HostDegraded {
				state += "\tdegraded; no new boxes or runners"
			}
			if h.Problem == contract.HostProblemCreateFailed {
				state += "\tbox creation failed; clear with host set --clear-create-problem"
			}
			if h.Problem == contract.HostProblemShareUnavailable {
				state += "\tshared folder mount unavailable, so no session boxes (CI may run); fix FUSE, then host set --clear-create-problem"
			}
			for _, p := range h.RunnerImages {
				state += fmt.Sprintf("\tCI runner image of room %s (%s) failed to build, jobs use the shared image: %s", p.Room, p.Image, p.Reason)
			}
			if h.Leftovers > 0 {
				state += fmt.Sprintf("\t%d left from deleted rooms", h.Leftovers)
			}
			if h.Pool != "" {
				state += "\tpool " + h.Pool
			}
			if h.ExecOnly {
				state += "\texec only"
			} else if h.Name == contract.ControlPlane {
				state += "\tengine machine: host exec and room folders only"
				if engine != "" {
					state += "; same machine as " + engine
				}
			} else {
				state += "\tagents " + onOff(h.Agents) + "\tci " + onOff(h.CI)
				if h.CI {
					state += fmt.Sprintf(" (scale %d, %d runners now)", h.CIScale, h.Runners)
				}
				if h.EngineHost {
					state += "\tengine host"
				}
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", h.Name, used, total, state); err != nil {
				return err
			}
		}
		return nil
	}}
	var discardBoxes bool
	removeHost := &cobra.Command{Use: "remove NAME [--discard-boxes]", Short: "Remove a host with nothing on it", Long: "Remove a host and its forward. It refuses while a box is on the host, and says what is. A pool's node goes back through its script.\n\n" +
		"With --discard-boxes it removes the host with its boxes, as a host lost for good, whether or not the host answers. Every box on it becomes failed with reason \"host removed\", and its volume is deleted: work that was not pushed is lost. On a host that does not answer, the volumes go with the machine. Each session's parent hears of the failure once, and `dyna session restart SESSION` gives the session a new, empty box on another host. A session being closed is closed without its archive. It prints the sessions to restart.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().HostRemove(cmd.Context(), contract.HostRemoveRequest{Host: contract.HostName(args[0]), DiscardBoxes: discardBoxes})
		if err != nil {
			return err
		}
		for _, session := range r.Failed {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), session); err != nil {
				return err
			}
		}
		return nil
	}}
	removeHost.Flags().BoolVar(&discardBoxes, "discard-boxes", false, "Fail every box on the host and delete its volume; work not pushed is lost")
	host.AddCommand(add, setHost, limitHost, list, removeHost, relocateEngineCommand())
	root.AddCommand(host)
	root.AddCommand(poolCommand(getClient))
	root.AddCommand(serviceCommand(getClient))
	root.AddCommand(profileCommand(getClient), accountCommand(getClient), addonCommand(getClient))
	root.AddCommand(discordCommand(getClient))

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
	addRepo := &cobra.Command{Use: "add ROOM REPO", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().RoomRepoAdd(cmd.Context(), contract.RoomRepoAddRequest{Room: contract.RoomID(args[0]), Repo: contract.RepoName(args[1]), Default: makeDefault})
		return err
	}}
	addRepo.Flags().BoolVar(&makeDefault, "default", false, "Make this the room default")
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
	root.AddCommand(room)

	session := &cobra.Command{Use: "session"}
	session.AddCommand(setAccountCommand(getClient))
	session.AddCommand(&cobra.Command{Use: "stop SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionStop(cmd.Context(), contract.SessionStopRequest{Session: contract.SessionID(args[0])})
		if err != nil {
			return err
		}
		if !result.Stopping {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No turn is running.")
		}
		return err
	}})
	var roomID, name, profile, goal, doneWhen, brief, sessionRepo, parent, size, composeName, sessionHost string
	var sameHost bool
	var gpu int
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().SessionStart(cmd.Context(), contract.SessionStartRequest{Room: contract.RoomID(roomID), Name: name, Parent: contract.SessionID(parent), Repo: contract.RepoName(sessionRepo), Profile: contract.ProfileName(profile), Goal: goal, DoneWhen: doneWhen, Brief: brief, Size: contract.BoxSizeName(size), SameHost: sameHost, Host: contract.HostName(sessionHost), Compose: composeName, GPU: contract.GPUGiB(gpu)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Session)
		}
		return err
	}}
	start.Flags().StringVar(&roomID, "room", "", "Room ID")
	start.Flags().StringVar(&name, "name", "", "Session name")
	start.Flags().StringVar(&profile, "profile", "", "Profile")
	start.Flags().StringVar(&sessionRepo, "repo", "", "Session repository")
	start.Flags().StringVar(&parent, "parent", "", "Parent session ID")
	start.Flags().StringVar(&goal, "goal", "", "Goal")
	start.Flags().StringVar(&doneWhen, "done-when", "", "When the goal is done")
	start.Flags().StringVar(&brief, "brief", "", "Brief")
	start.Flags().StringVar(&size, "size", "", "Box size for a lead: agent, small, medium or large")
	start.Flags().BoolVar(&sameHost, "same-host", false, "Place the box on its parent’s host")
	start.Flags().StringVar(&sessionHost, "host", "", "Pin the new box to a named host; cannot combine with same-host")
	start.Flags().StringVar(&composeName, "compose", "", "Compose file, by name; without it the file named default, if any")
	start.Flags().IntVar(&gpu, "gpu", 0, "GiB of GPU memory the box asks for on a host card; 0 asks for none")
	closeCmd := &cobra.Command{Use: "close SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		closed, err := getClient().SessionClose(cmd.Context(), contract.SessionCloseRequest{Session: contract.SessionID(args[0])})
		if err == nil && len(closed.Handed) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Handed to %s: %s\n", closed.To, dynaroom.ResponsibilityNames(closed.Handed))
		}
		if err == nil && len(closed.Folded) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Folds into %s's goal as items, with their open items as subtasks: %s.\n", closed.To, dynaroom.GoalNames(closed.Folded))
		}
		if err == nil && len(closed.Unheld) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Left with no holder, because %s is root and root holds no goals: %s. goal list --session %s shows them; goal done ends them, and settles their open items.\n", closed.To, dynaroom.GoalNames(closed.Unheld), closed.To)
		}
		return err
	}}
	restart := &cobra.Command{Use: "restart SESSION", Short: "Replace a failed session box", Long: "Replace a failed session box. The old volume and unpushed work are lost. Removes the container and workspace/stack volumes on every known current or prior host before resetting the box. If cleanup on any of those hosts fails, as when it does not answer, the box stays failed and the error names the host; run restart again once that host answers (host list shows it). Deletion may already be partial; it is not rolled back.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionRestart(cmd.Context(), contract.SessionRestartRequest{Session: contract.SessionID(args[0])})
		return err
	}}
	var refreshRoom string
	refresh := &cobra.Command{Use: "refresh [SESSION]", Short: "Mark a box to restart once nothing needs it", Long: "Mark a running box to restart once no turn, job or terminal needs it. It keeps its volume, unlike session restart, which discards it.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if (len(args) == 1) == (refreshRoom != "") {
			return fmt.Errorf("give a session or --room")
		}
		request := contract.SessionRefreshRequest{Room: contract.RoomID(refreshRoom)}
		if len(args) == 1 {
			request.Session = contract.SessionID(args[0])
		}
		r, err := getClient().SessionRefresh(cmd.Context(), request)
		if err != nil {
			return err
		}
		if len(r.Marked) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No running box to mark.")
		}
		for _, id := range r.Marked {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), id); err != nil {
				return err
			}
		}
		return err
	}}
	refresh.Flags().StringVar(&refreshRoom, "room", "", "Mark every open session in this room")
	setProfile := &cobra.Command{Use: "set-profile SESSION PROFILE", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetProfile(cmd.Context(), contract.SessionSetProfileRequest{Session: contract.SessionID(args[0]), Profile: contract.ProfileName(args[1])})
		return err
	}}
	setBranch := &cobra.Command{Use: "set-branch SESSION BRANCH", Short: "Make BRANCH the session's branch on its repository (owner socket only; renames nothing on GitHub)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetBranch(cmd.Context(), contract.SessionSetBranchRequest{Session: contract.SessionID(args[0]), Branch: args[1]})
		return err
	}}
	session.AddCommand(&cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionList(cmd.Context(), contract.SessionListRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Sessions)
	}}, &cobra.Command{Use: "responsibilities SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionResponsibilities(cmd.Context(), contract.SessionResponsibilitiesRequest{Session: contract.SessionID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}, &cobra.Command{Use: "move SESSION NEW_PARENT", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionMove(cmd.Context(), contract.SessionMoveRequest{Session: contract.SessionID(args[0]), NewParent: contract.SessionID(args[1])})
		return err
	}})
	var composeRoom, composeRepo string
	composeList := &cobra.Command{Use: "compose-list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().ComposeList(cmd.Context(), contract.ComposeListRequest{Room: contract.RoomID(composeRoom), Repo: contract.RepoName(composeRepo)})
		if err != nil {
			return err
		}
		for _, file := range r.Files {
			line := file.Name + " (" + string(file.Level) + ")"
			if file.Problem != "" {
				line += ": " + file.Problem
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return err
			}
		}
		return nil
	}}
	composeList.Flags().StringVar(&composeRoom, "room", "", "Room ID")
	composeList.Flags().StringVar(&composeRepo, "repo", "", "Repository a child would use")
	session.AddCommand(start, closeCmd, restart, refresh, setProfile, setBranch, composeList)
	root.AddCommand(session)
	var postRoom, htmlPath, update string
	var allowNet bool
	var forward string
	var mentions, paths []string
	postCmd := &cobra.Command{Use: "post TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		upload := func(ctx context.Context, path string) (contract.Attachment, error) {
			return c.Upload(ctx, contract.RoomID(postRoom), path)
		}
		request, checks, err := (post.Preparer{Upload: upload, Render: visual.Preview}).Prepare(cmd.Context(), tools.PostInput{Room: contract.RoomID(postRoom), Text: args[0], Mentions: mentions, Paths: paths, HTML: htmlPath, AllowNet: allowNet, VisualUpdate: contract.EventID(update), Forward: contract.EventID(forward)})
		if err != nil {
			return err
		}
		printVisualChecks(cmd.ErrOrStderr(), checks)
		request.ToolCallID = os.Getenv(contract.ToolCallIDEnv)
		r, err := c.Post(cmd.Context(), request)
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Event)
		}
		return err
	}}
	postCmd.Flags().StringVar(&htmlPath, "html", "", "Attach an HTML visual card")
	postCmd.Flags().StringVar(&update, "update", "", "Add a version to a visual post")
	postCmd.Flags().BoolVar(&allowNet, "allow-net", false, "Allow HTTPS resources in the visual")
	postCmd.Flags().StringArrayVar(&paths, "attach", nil, "Attach a file (repeatable)")
	postCmd.Flags().StringVar(&forward, "forward", "", "Forward a post by ID, with TEXT as your note (may be empty)")
	postCmd.Flags().StringVar(&postRoom, "room", "", "Room ID")
	postCmd.Flags().StringSliceVar(&mentions, "mention", nil, "Session mentions")
	root.AddCommand(postCmd, visualCommand())
	root.AddCommand(goalCommand(getClient))
	contextCmd := &cobra.Command{Use: "context"}
	contextCmd.AddCommand(&cobra.Command{Use: "show SECTION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().ContextSnapshot(cmd.Context(), contract.ContextSnapshotRequest{Section: contract.ContextSectionKey(args[0])})
		if err != nil {
			return err
		}
		for _, section := range result.Sections {
			if _, err = fmt.Fprintln(cmd.OutOrStdout(), section.Text); err != nil {
				return err
			}
		}
		return nil
	}})
	root.AddCommand(contextCmd)

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
