package main

import (
	"fmt"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

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

func hostCommand(getClient func() client) *cobra.Command {
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
	return host
}
