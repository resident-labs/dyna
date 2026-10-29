// Package machine exposes the machines registry as `dyna machine`: the engine
// host, the machines that run agents, and the machines that run local CI.
package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/websvc"
)

// NewCommand builds the `dyna machine` group.
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "machine",
		Short: "List and change the machines that run CI and agents",
		Long: "The registry holds this machine, marked local, and each machine that Dyna\n" +
			"can use. A machine has one or more roles: engine (this machine only),\n" +
			"agents, and ci. A ci machine needs an SSH destination and at least one CI\n" +
			"slot. `dyna git ci node` still works and forwards to these commands.\n\n" +
			"`dyna machine probe` reads a machine's capabilities and load over SSH. The\n" +
			"engine also probes every enabled machine when it starts, when a machine\n" +
			"changes, and every five minutes.",
		Args: cobra.NoArgs,
	}
	root.AddCommand(listCommand(), showCommand(), addCommand(), editCommand(), probeCommand(), removeCommand())
	return root
}

func store() machines.Store { return machines.Store{} }

var saveMachine = func(ctx context.Context, ref string, patch websvc.MachinePatch) (websvc.MachineDetail, error) {
	var detail websvc.MachineDetail
	err := engineclient.New("").Call(ctx, "SaveMachine", []any{ref, patch}, &detail)
	return detail, err
}

// prober is the prober the CLI saves through. It reads the same disk floor as
// the engine, so a probe from the command line marks a CI machine that is low
// on disk exactly as the engine's own probe does.
func prober() machines.Prober {
	return machines.Prober{Store: store(), DiskFloorBytes: diskFloorBytes()}
}

// diskFloorBytes is the free space a CI machine must keep. A settings file that
// cannot be read keeps the default floor.
func diskFloorBytes() uint64 {
	values, err := settings.Load("")
	if err != nil {
		return settings.Defaults().CIDiskFloorBytes()
	}
	return values.CIDiskFloorBytes()
}

func listCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List machines, this machine first",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			list, err := store().List(c.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), list)
			}
			return writeTable(c.OutOrStdout(), list)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machines as JSON")
	return cmd
}

// writeTable prints one row per machine. The LOCAL column marks the machine
// that runs this engine.
func writeTable(w io.Writer, list []machines.Machine) error {
	out := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "NAME\tROLES\tSTATUS\tSSH\tROOMS\tSLOTS\tENABLED\tLOCAL")
	for _, m := range list {
		ssh, local := m.SSH, ""
		if ssh == "" {
			ssh = "local"
		}
		if m.Local {
			local = "*"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%d\t%t\t%s\n", m.Name, m.RoleNames(), m.Status(), ssh, roomList(m.Rooms), m.CISlots, m.Enabled, local)
	}
	return out.Flush()
}

// hostChecksWho names who allows host checks, for `dyna machine show`. A room
// root's grant names its room, which this command reads by name where it can.
func hostChecksWho(m machines.Machine) string {
	if len(m.HostChecksGrants) == 0 {
		return "no one"
	}
	who := make([]string, 0, len(m.HostChecksGrants))
	for _, grant := range m.HostChecksGrants {
		if grant.By == machines.GrantorOwner {
			who = append(who, "owner")
			continue
		}
		who = append(who, "root of room "+roomName(grant.Room))
	}
	return strings.Join(who, ", ")
}

// roomName names a room by its readable name, and keeps its ID when no such
// room is stored.
func roomName(id string) string {
	rec, err := room.DefaultStore().Resolve(id)
	if err != nil || rec.Name == "" {
		return id
	}
	return rec.Name
}

func showCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one machine",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := store().Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), m)
			}
			out := c.OutOrStdout()
			ssh := m.SSH
			if ssh == "" {
				ssh = "local"
			}
			fmt.Fprintf(out, "Name:     %s\n", m.Name)
			fmt.Fprintf(out, "ID:       %s\n", m.ID)
			fmt.Fprintf(out, "Roles:    %s\n", m.RoleNames())
			fmt.Fprintf(out, "SSH:      %s\n", ssh)
			if !m.Local {
				fmt.Fprintf(out, "SSH connect timeout: %s\n", m.ConnectTimeout())
			}
			fmt.Fprintf(out, "CI slots: %d\n", m.CISlots)
			if m.HasRole(machines.RoleCI) {
				fmt.Fprintf(out, "Host checks: %t (%s)\n", m.HasHostChecksGrants(), hostChecksWho(m))
				fmt.Fprintf(out, "Rooms:    %s\n", roomList(m.Rooms))
				fmt.Fprintf(out, "Suites:   %s\n", suiteList(m.Suites))
			}
			fmt.Fprintf(out, "Enabled:  %t\n", m.Enabled)
			fmt.Fprintf(out, "Local:    %t\n", m.Local)
			fmt.Fprintf(out, "Status:   %s\n", m.Status())
			fmt.Fprintf(out, "Probed:   %s\n", stamp(m.LastProbe))
			fmt.Fprintf(out, "Seen:     %s\n", stamp(m.LastSeen))
			if m.ProbeError != "" {
				fmt.Fprintf(out, "Error:    %s\n", m.ProbeError)
			}
			writeCapabilities(out, m)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the machine as JSON")
	return cmd
}

// writeCapabilities prints what the last probe read, or says that no probe
// has reached the machine.
func writeCapabilities(out io.Writer, m machines.Machine) {
	caps, load := m.Capabilities, m.Load
	if m.LastSeen.IsZero() {
		fmt.Fprintln(out, "System:   not probed yet")
		return
	}
	system := caps.OS
	if caps.Arch != "" {
		system += "/" + caps.Arch
	}
	fmt.Fprintf(out, "System:   %s, %d CPUs, %s memory, %s available\n",
		system, caps.CPUs, size(caps.MemTotal), size(caps.MemAvailable))
	fmt.Fprintf(out, "GPU:      %t\n", caps.GPU)
	fmt.Fprintf(out, "Runtime:  %s\n", joinOr(caps.Containers.Names(), "none"))
	fmt.Fprintf(out, "Computers: %t, stream: %t\n", caps.Computers.Ready(), caps.Computers.Stream)
	fmt.Fprintf(out, "Dyna:     %s\n", orNone(caps.DynaVersion))
	if caps.Disk.Total > 0 {
		fmt.Fprintf(out, "Disk:     %s free of %s on /var/tmp\n", size(caps.Disk.Free), size(caps.Disk.Total))
	}
	// A CI machine below the floor keeps its CI work until space is freed, so
	// the reason is named, not only the health.
	if m.HoldsCI() {
		fmt.Fprintf(out, "CI:       held; free space is below the %s floor of machines.ciDiskFloorGB\n", size(diskFloorBytes()))
	}
	if load.CPUPercent != nil {
		fmt.Fprintf(out, "Load:     %.2f %.2f %.2f, CPU %.0f%%, %s free\n",
			load.LoadAvg[0], load.LoadAvg[1], load.LoadAvg[2], *load.CPUPercent, size(load.MemAvailable))
		return
	}
	fmt.Fprintf(out, "Load:     %.2f %.2f %.2f, %s free\n",
		load.LoadAvg[0], load.LoadAvg[1], load.LoadAvg[2], size(load.MemAvailable))
}

func probeCommand() *cobra.Command {
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:   "probe [<name>|--all]",
		Short: "Read a machine's capabilities and load, and store them",
		Long: "Probe one machine, or every enabled machine with --all, and store what\n" +
			"the probe read. A remote machine is read over SSH with no credential. A\n" +
			"failed probe keeps the last capabilities it read, and marks the machine\n" +
			"offline. A disabled machine is not probed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return errors.New("probe: give a machine name or --all, not both")
			}
			if !all && len(args) == 0 {
				return errors.New("probe: give a machine name or --all")
			}
			targets := args
			if all {
				list, err := store().List(c.Context())
				if err != nil {
					return err
				}
				targets = nil
				for _, m := range list {
					if m.Enabled {
						targets = append(targets, m.Name)
					}
				}
			}
			p, out := prober(), c.OutOrStdout()
			var failures []error
			for _, name := range targets {
				m, err := store().Get(c.Context(), name)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				started := time.Now()
				stored, err := p.ProbeAndSave(c.Context(), m)
				if errors.Is(err, machines.ErrDisabled) {
					failures = append(failures, fmt.Errorf("%s is disabled; enable it with dyna machine edit %s --enable", name, name))
					continue
				}
				if err != nil {
					failures = append(failures, err)
					continue
				}
				if !asJSON {
					writeProbe(out, stored, time.Since(started))
				}
			}
			if asJSON {
				list, err := store().List(c.Context())
				if err != nil {
					return err
				}
				if err := jsonout.Write(out, list); err != nil {
					return err
				}
			}
			return errors.Join(failures...)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "probe every enabled machine")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the probed machines as JSON")
	return cmd
}

// writeProbe prints one probe result. A machine that could not be reached
// prints as a result with its error, not as a command failure.
func writeProbe(out io.Writer, m machines.Machine, took time.Duration) {
	fmt.Fprintf(out, "%s: %s in %s\n", m.Name, m.Status(), took.Round(time.Millisecond))
	if m.ProbeError != "" {
		fmt.Fprintf(out, "  Error: %s\n", m.ProbeError)
		return
	}
	writeCapabilities(out, m)
}

func addCommand() *cobra.Command {
	var ssh string
	var roleNames []string
	var slots int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a machine, or update the machine with this name",
		Long: "Add a machine. Its name must be new: use edit to change a stored machine.\n" +
			"The default role is ci with one slot. A positive --ci-slots adds the ci\n" +
			"role.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			roles, err := parseRoles(roleNames)
			if err != nil {
				return err
			}
			if len(roles) == 0 {
				roles = []machines.Role{machines.RoleCI}
			}
			if c.Flags().Changed("ci-slots") && slots > 0 && !slices.Contains(roles, machines.RoleCI) {
				roles = append(roles, machines.RoleCI)
			}
			enabled := true
			patch := websvc.MachinePatch{Create: true, Enabled: &enabled, Roles: &roles, SSH: &ssh}
			if slices.Contains(roles, machines.RoleCI) {
				patch.CISlots = &slots
			}
			saved, err := saveMachine(c.Context(), args[0], patch)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved machine %s with roles %s.\n", saved.Name, roleList(saved.Roles))
			return nil
		},
	}
	cmd.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host")
	cmd.Flags().StringArrayVar(&roleNames, "role", nil, "role to give the machine: ci or agents (repeatable; default ci)")
	cmd.Flags().IntVar(&slots, "ci-slots", 1, "maximum concurrent CI checks; a positive value adds the ci role")
	return cmd
}

func editCommand() *cobra.Command {
	var rename, ssh string
	var roleNames []string
	var slots int
	var maxIOHeavy, maxDiskPercent int
	var enable, disable bool
	var allowHostChecks, disallowHostChecks bool
	var rooms []string
	var containerSocket string
	var connectTimeout time.Duration
	var checkTmp string
	var suites []string
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a machine",
		Long: "Change a machine. --role replaces every role, so give all of the roles the\n" +
			"machine should keep. A positive --ci-slots adds the ci role, and a machine\n" +
			"that gets the ci role without a slot count gets one slot.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			current, err := store().Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			patch := websvc.MachinePatch{}
			changed := false
			roles := append([]machines.Role(nil), current.Roles...)
			if c.Flags().Changed("rename") {
				patch.Name, changed = &rename, true
			}
			if c.Flags().Changed("ssh") {
				patch.SSH, changed = &ssh, true
			}
			if c.Flags().Changed("role") {
				if roles, err = parseRoles(roleNames); err != nil {
					return err
				}
				patch.Roles, changed = &roles, true
			}
			if c.Flags().Changed("ci-slots") {
				patch.CISlots, changed = &slots, true
				// A positive slot count gives the machine the ci role it needs
				// to hold those slots.
				if slots > 0 && !slices.Contains(roles, machines.RoleCI) {
					roles = append(roles, machines.RoleCI)
					patch.Roles = &roles
				}
			}
			if enable || disable {
				patch.Enabled, changed = &enable, true
			}
			if c.Flags().Changed("rooms") {
				list, err := resolveRooms(rooms)
				if err != nil {
					return err
				}
				patch.Rooms, changed = &list, true
			}
			if c.Flags().Changed("suites") {
				list := parseSuites(suites)
				patch.Suites, changed = &list, true
			}
			if c.Flags().Changed("container-socket") {
				if containerSocket != "" && !strings.HasPrefix(containerSocket, "unix://") {
					return fmt.Errorf("container socket must be a unix:// path, such as unix:///var/run/docker.sock")
				}
				patch.ContainerSocket, changed = &containerSocket, true
			}
			if c.Flags().Changed("ssh-connect-timeout") {
				seconds, err := connectTimeoutSeconds(connectTimeout)
				if err != nil {
					return err
				}
				patch.SSHConnectTimeoutSeconds, changed = &seconds, true
			}
			if c.Flags().Changed("check-tmp") {
				patch.CheckTmp, changed = &checkTmp, true
			}
			if c.Flags().Changed("max-io-heavy") {
				patch.MaxIOHeavy, changed = &maxIOHeavy, true
			}
			if c.Flags().Changed("max-disk-percent") {
				patch.MaxDiskPercent, changed = &maxDiskPercent, true
			}
			if allowHostChecks || disallowHostChecks {
				patch.AllowHostChecks, changed = &allowHostChecks, true
			}
			if !changed {
				return fmt.Errorf("edit %s: give a machine, host-check, room or suite setting to change", args[0])
			}
			saved, err := saveMachine(c.Context(), current.ID, patch)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved machine %s with roles %s.\n", saved.Name, roleList(saved.Roles))
			return nil
		},
	}
	cmd.Flags().StringVar(&rename, "rename", "", "new name for the machine")
	cmd.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host")
	cmd.Flags().StringArrayVar(&roleNames, "role", nil, "replace the roles with these: engine, agents or ci (repeatable)")
	cmd.Flags().IntVar(&slots, "ci-slots", 0, "maximum concurrent CI checks; a positive value adds the ci role")
	cmd.Flags().BoolVar(&enable, "enable", false, "allow work on the machine")
	cmd.Flags().BoolVar(&disable, "disable", false, "stop new work on the machine")
	cmd.Flags().BoolVar(&allowHostChecks, "allow-host-checks", false, "allow repository checks to run outside containers")
	cmd.Flags().BoolVar(&disallowHostChecks, "disallow-host-checks", false, "require repository checks to use containers")
	cmd.Flags().StringSliceVar(&rooms, "rooms", nil, "limit local CI to these rooms (comma separated; empty allows every room)")
	cmd.Flags().StringSliceVar(&suites, "suites", nil, "limit local CI to these suites (comma separated; empty allows every suite)")
	cmd.Flags().StringVar(&containerSocket, "container-socket", "", "container runtime socket for workflow jobs (unix:// path; empty uses Docker's)")
	cmd.Flags().DurationVar(&connectTimeout, "ssh-connect-timeout", 0, "how long ssh waits to connect to the machine, such as 60s (0 restores the default of 10s)")
	cmd.Flags().StringVar(&checkTmp, "check-tmp", "", "folder that holds each host check's temporary folder, such as /dev/shm (empty uses the machine's TMPDIR, else /tmp)")
	cmd.Flags().IntVar(&maxIOHeavy, "max-io-heavy", 0, "IO-heavy jobs and checks this machine runs at once (0: one per 16 CPUs, at least 1)")
	cmd.Flags().IntVar(&maxDiskPercent, "max-disk-percent", 0, "used-disk percent above which the machine admits no new work (0: 90)")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive("allow-host-checks", "disallow-host-checks")
	return cmd
}

// connectTimeoutSeconds turns the flag into whole seconds, the unit ssh reads.
// Zero restores the default.
func connectTimeoutSeconds(timeout time.Duration) (int, error) {
	if timeout < 0 || timeout%time.Second != 0 || timeout > machines.MaxSSHConnectTimeout {
		return 0, fmt.Errorf("ssh connect timeout must be whole seconds, such as 60s, up to %s", machines.MaxSSHConnectTimeout)
	}
	return int(timeout / time.Second), nil
}

func removeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a machine; this machine cannot be removed",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// The engine removes the machine, as it owns every other change to one.
			// The command names the machine it read by ID, because a name can equal
			// another machine's ID and the engine resolves IDs first.
			m, err := store().Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			if _, err := saveMachine(c.Context(), m.ID, websvc.MachinePatch{Remove: true}); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Removed machine %s.\n", m.Name)
			return nil
		},
	}
}

// roleList lists roles as the saved message spells them.
func roleList(roles []machines.Role) string {
	names := make([]string, len(roles))
	for i, role := range roles {
		names[i] = string(role)
	}
	return strings.Join(names, ", ")
}

func parseRoles(names []string) ([]machines.Role, error) {
	roles := make([]machines.Role, 0, len(names))
	for _, name := range names {
		role, err := machines.ParseRole(name)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// stamp prints a probe time, or a dash before the first probe.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.DateTime)
}

// size prints a byte count in the largest unit that keeps it readable.
func size(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, exp := float64(bytes), 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGTP"[exp-1])
}

// joinOr joins names for one line, or prints the fallback when there is none.
func joinOr(names []string, fallback string) string {
	if len(names) == 0 {
		return fallback
	}
	return strings.Join(names, ",")
}

// resolveRooms reads the room names or IDs a command line names and stores their
// IDs, so a renamed room keeps taking the machine's work and a typo cannot limit
// a machine to a room that does not exist.
func resolveRooms(names []string) ([]string, error) {
	rooms := room.DefaultStore()
	ids := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		record, err := rooms.Resolve(name)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, record.ID) {
			ids = append(ids, record.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// roomList words a machine's room limit: the names of the rooms it takes work
// from, or every room when it names none.
func roomList(ids []string) string {
	if len(ids) == 0 {
		return "every room"
	}
	rooms := room.DefaultStore()
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if record, err := rooms.Resolve(id); err == nil && record.Name != "" {
			names = append(names, record.Name)
			continue
		}
		names = append(names, id)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// parseSuites reads the suite names a command line names. A suite ID comes from
// a repository definition, so there is nothing to resolve: the list only has to
// be trimmed, sorted and free of repeats.
func parseSuites(names []string) []string {
	suites := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(suites, name) {
			suites = append(suites, name)
		}
	}
	slices.Sort(suites)
	return suites
}

// suiteList words a machine's suite limit: the suites it takes work for, or
// every suite when it names none.
func suiteList(suites []string) string {
	if len(suites) == 0 {
		return "every suite"
	}
	return strings.Join(suites, ", ")
}

// orNone prints a value, or a dash when the probe read none.
func orNone(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
