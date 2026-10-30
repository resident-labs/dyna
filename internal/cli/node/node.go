// Package node exposes the nodes registry as `dyna node`: the engine
// host, marked local, and every other node Dyna reaches over SSH.
package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/nodehistory"
	"dyna-agent/internal/room"
	dynastore "dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// NewCommand builds the `dyna node` group.
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:        "node",
		Short:      "List and change the nodes that run CI and jobs",
		SuggestFor: []string{"machine"},
		Long: "The registry holds this node, marked local, and each node that Dyna\n" +
			"can use. A node that is enabled and whose room list allows a room takes\n" +
			"that room's local CI work and jobs; the admission queue bounds how much\n" +
			"runs. A node other than this one needs an SSH destination.\n\n" +
			"`dyna node probe` reads a node's capabilities and load over SSH. The\n" +
			"engine also probes every enabled node when it starts, when a node\n" +
			"changes, and every five minutes.",
		Args: cobra.NoArgs,
	}
	root.AddCommand(listCommand(), showCommand(), addCommand(), editCommand(), probeCommand(), removeCommand())
	return root
}

func store() machines.Store { return machines.Store{} }

var saveMachine = func(ctx context.Context, ref string, patch websvc.MachinePatch) (websvc.MachineDetail, error) {
	var detail websvc.MachineDetail
	err := engineclient.New("").Call(ctx, "SaveNode", []any{ref, patch}, &detail)
	return detail, err
}

// prober is the prober the CLI saves through. It reads the same disk rule as
// the engine, so a probe from the command line stores what the engine's own
// probe stores.
func prober() machines.Prober {
	return machines.Prober{Store: store(), Record: nodehistory.Recorder(dynastore.DataDir())}
}

func listCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List nodes, this node first",
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "print nodes as JSON")
	return cmd
}

// writeTable prints one row per node. The LOCAL column marks the node
// that runs this engine.
func writeTable(w io.Writer, list []machines.Machine) error {
	out := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "NAME\tSTATUS\tSSH\tROOMS\tENABLED\tLOCAL")
	for _, m := range list {
		ssh, local := m.SSH, ""
		if ssh == "" {
			ssh = "local"
		}
		if m.Local {
			local = "*"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%t\t%s\n", m.Name, m.Status(), ssh, roomList(m.Rooms), m.Enabled, local)
	}
	return out.Flush()
}

// hostChecksWho names who allows host checks, for `dyna node show`. A room
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
		Short: "Show one node",
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
			fmt.Fprintf(out, "SSH:      %s\n", ssh)
			if m.QuickSlots > 0 && m.QuickSlots != m.QuickSlotLimit() {
				fmt.Fprintf(out, "Quick slots: %d (%d used: memory limit)\n", m.QuickSlots, m.QuickSlotLimit())
			} else {
				fmt.Fprintf(out, "Quick slots: %d\n", m.QuickSlotLimit())
			}
			if !m.Local {
				fmt.Fprintf(out, "SSH connect timeout: %s\n", m.ConnectTimeout())
			}
			if len(m.Resources) > 0 {
				parts := make([]string, 0, len(m.Resources))
				for _, name := range slices.Sorted(maps.Keys(m.Resources)) {
					parts = append(parts, fmt.Sprintf("%s=%d", name, m.Resources[name]))
				}
				fmt.Fprintf(out, "Resources: %s\n", strings.Join(parts, ", "))
			}
			if len(m.Mounts) > 0 {
				parts := make([]string, 0, len(m.Mounts))
				for _, mount := range m.Mounts {
					parts = append(parts, fmt.Sprintf("%s=%s:%s (room %s)", mount.Name, mount.Source, mount.Mode, roomName(mount.Room)))
				}
				fmt.Fprintf(out, "Mounts:   %s\n", strings.Join(parts, ", "))
			}
			if len(m.Devices) > 0 {
				parts := make([]string, 0, len(m.Devices))
				for _, device := range m.Devices {
					parts = append(parts, fmt.Sprintf("%s (room %s)", device.Path, roomName(device.Room)))
				}
				fmt.Fprintf(out, "Devices:  %s\n", strings.Join(parts, ", "))
			}
			fmt.Fprintf(out, "Host checks: %t (%s)\n", m.HasHostChecksGrants(), hostChecksWho(m))
			fmt.Fprintf(out, "Rooms:    %s\n", roomList(m.Rooms))
			changes, err := store().LatestFieldChanges(c.Context(), m.ID)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				fmt.Fprintln(out, "Setting changes: unknown (no recorded changes)")
			}
			for _, change := range changes {
				actor := string(change.Actor.Role)
				if change.Actor.Session != "" {
					actor += " " + change.Actor.Session
				}
				if change.Actor.Room != "" {
					actor += " (room " + roomName(change.Actor.Room) + ")"
				}
				if actor == "" {
					actor = "unknown"
				}
				fmt.Fprintf(out, "Changed %s: %s by %s\n", change.Field, change.Changed.Format(time.RFC3339), actor)
			}
			fmt.Fprintf(out, "Owner labels:  %s\n", labelList(m.Labels))
			fmt.Fprintf(out, "Probed labels: %s\n", joinOr(m.ProbedLabels(), "none"))
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the node as JSON")
	return cmd
}

// writeCapabilities prints what the last probe read, or says that no probe
// has reached the node.
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
		rule := "keeps " + m.DiskKeeps()
		if block := m.DiskBlock(); block != "" {
			rule = block
		}
		fmt.Fprintf(out, "Disk:     %s free of %s on /var/tmp · %s\n", size(caps.Disk.Free), size(caps.Disk.Total), rule)
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
		Short: "Read a node's capabilities and load, and store them",
		Long: "Probe one node, or every enabled node with --all, and store what\n" +
			"the probe read. A remote node is read over SSH with no credential. A\n" +
			"failed probe keeps the last capabilities it read, and marks the node\n" +
			"offline. A disabled node is not probed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return errors.New("probe: give a node name or --all, not both")
			}
			if !all && len(args) == 0 {
				return errors.New("probe: give a node name or --all")
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
					failures = append(failures, fmt.Errorf("%s is disabled; enable it with dyna node edit %s --enable", name, name))
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
	cmd.Flags().BoolVar(&all, "all", false, "probe every enabled node")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the probed nodes as JSON")
	return cmd
}

// writeProbe prints one probe result. A node that could not be reached
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
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a node, or update the node with this name",
		Long: "Add a node. Its name must be new: use edit to change a stored node.\n" +
			"A node other than this one needs an SSH destination.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			enabled := true
			patch := websvc.MachinePatch{Create: true, Enabled: &enabled, SSH: &ssh}
			saved, err := saveMachine(c.Context(), args[0], patch)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved node %s.\n", saved.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host")
	return cmd
}

func editCommand() *cobra.Command {
	var rename, ssh string
	var maxIOHeavy, maxDiskPercent, quickSlots int
	var resourceEdits, labels, unlabels, mountEdits, deviceEdits, unmountEdits, undeviceEdits []string
	var registrationRoom string
	var enable, disable bool
	var allowHostChecks, disallowHostChecks bool
	var rooms []string
	var containerSocket string
	var connectTimeout time.Duration
	var checkTmp string
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a node",
		Long: "Change a node's route, switch, room list, labels or limits.\n" +
			"A node that is enabled and whose room list allows a room takes that\n" +
			"room's local CI work and jobs.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			current, err := store().Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			patch := websvc.MachinePatch{}
			changed := false
			if c.Flags().Changed("rename") {
				patch.Name, changed = &rename, true
			}
			if c.Flags().Changed("ssh") {
				patch.SSH, changed = &ssh, true
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
			if c.Flags().Changed("label") || c.Flags().Changed("unlabel") {
				list, err := editLabels(current.Labels, labels, unlabels)
				if err != nil {
					return err
				}
				patch.Labels, changed = &list, true
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
			if c.Flags().Changed("quick-slots") {
				patch.QuickSlots, changed = &quickSlots, true
			}
			if c.Flags().Changed("max-io-heavy") {
				patch.MaxIOHeavy, changed = &maxIOHeavy, true
			}
			if c.Flags().Changed("max-disk-percent") {
				patch.MaxDiskPercent, changed = &maxDiskPercent, true
			}
			if c.Flags().Changed("resource") {
				updated := maps.Clone(current.Resources)
				if updated == nil {
					updated = make(map[machines.ResourceName]int)
				}
				for _, edit := range resourceEdits {
					name, rawCount, ok := strings.Cut(edit, "=")
					if !ok {
						return fmt.Errorf("resource must be NAME=COUNT, not %q", edit)
					}
					parsed, err := machines.ParseResourceName(name)
					if err != nil {
						return err
					}
					count, err := strconv.Atoi(rawCount)
					if err != nil || count < 0 {
						return fmt.Errorf("resource count must be a non-negative integer, not %q", rawCount)
					}
					if count == 0 {
						delete(updated, parsed)
					} else {
						updated[parsed] = count
					}
				}
				patch.Resources, changed = &updated, true
			}
			if c.Flags().Changed("mount") || c.Flags().Changed("device") || c.Flags().Changed("unmount") || c.Flags().Changed("undevice") {
				if !c.Flags().Changed("room") || registrationRoom == "" {
					return fmt.Errorf("--mount, --unmount, --device and --undevice need --room")
				}
				ids, err := resolveRooms([]string{registrationRoom})
				if err != nil {
					return err
				}
				roomID := ids[0]
				if c.Flags().Changed("mount") {
					updates := make([]machines.NamedMount, 0, len(mountEdits))
					for _, edit := range mountEdits {
						mount, err := machines.ParseNamedMount(edit, roomID)
						if err != nil {
							return err
						}
						updates = append(updates, mount)
					}
					patch.Mounts = &updates
				}
				if c.Flags().Changed("device") {
					updates := make([]machines.DeviceGrant, 0, len(deviceEdits))
					for _, edit := range deviceEdits {
						device, err := machines.ParseDeviceGrant(edit, roomID)
						if err != nil {
							return err
						}
						updates = append(updates, device)
					}
					patch.Devices = &updates
				}
				if c.Flags().Changed("unmount") {
					removals := make([]machines.MountRef, 0, len(unmountEdits))
					for _, edit := range unmountEdits {
						mount, err := machines.ParseMountRef(edit, roomID)
						if err != nil {
							return err
						}
						removals = append(removals, mount)
					}
					patch.RemoveMounts = &removals
				}
				if c.Flags().Changed("undevice") {
					removals := make([]machines.DeviceRef, 0, len(undeviceEdits))
					for _, edit := range undeviceEdits {
						device, err := machines.ParseDeviceRef(edit, roomID)
						if err != nil {
							return err
						}
						removals = append(removals, device)
					}
					patch.RemoveDevices = &removals
				}
				changed = true
			}
			if allowHostChecks || disallowHostChecks {
				patch.AllowHostChecks, changed = &allowHostChecks, true
			}
			if !changed {
				return fmt.Errorf("edit %s: give a node, host-check, room, label or limit setting to change", args[0])
			}
			saved, err := saveMachine(c.Context(), current.ID, patch)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved node %s.\n", saved.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&rename, "rename", "", "new name for the node")
	cmd.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host")
	cmd.Flags().BoolVar(&enable, "enable", false, "allow work on the node")
	cmd.Flags().BoolVar(&disable, "disable", false, "stop new work on the node")
	cmd.Flags().BoolVar(&allowHostChecks, "allow-host-checks", false, "allow repository checks to run outside containers")
	cmd.Flags().BoolVar(&disallowHostChecks, "disallow-host-checks", false, "require repository checks to use containers")
	cmd.Flags().StringSliceVar(&rooms, "rooms", nil, "limit local CI to these rooms (comma separated; empty allows every room)")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "add a label, such as ci to run local CI checks (repeatable)")
	cmd.Flags().StringArrayVar(&unlabels, "unlabel", nil, "remove a label (repeatable)")
	cmd.Flags().StringVar(&containerSocket, "container-socket", "", "container runtime socket for workflow jobs (unix:// path; empty uses Docker's)")
	cmd.Flags().DurationVar(&connectTimeout, "ssh-connect-timeout", 0, "how long ssh waits to connect to the node, such as 60s (0 restores the default of 10s)")
	cmd.Flags().StringVar(&checkTmp, "check-tmp", "", "folder that holds each host check's temporary folder, such as /dev/shm (empty uses the node's TMPDIR, else /tmp)")
	cmd.Flags().IntVar(&quickSlots, "quick-slots", 0, "quick jobs this node runs at once (0: automatic, bounded by memory)")
	cmd.Flags().IntVar(&maxIOHeavy, "max-io-heavy", 0, "IO-heavy jobs and checks this node runs at once (0: one per 16 CPUs, at least 1)")
	cmd.Flags().IntVar(&maxDiskPercent, "max-disk-percent", 0, "used-disk percent above which the node admits no new work (0: keep at least 20 GiB and 5% of the disk free, capped at 10% of the disk)")
	cmd.Flags().StringArrayVar(&resourceEdits, "resource", nil, "set a named resource count, such as ghidra=1 (repeatable; NAME=0 removes it)")
	cmd.Flags().StringArrayVar(&mountEdits, "mount", nil, "register NAME=HOST_PATH:ro|rw for container jobs (repeatable; needs --room)")
	cmd.Flags().StringArrayVar(&deviceEdits, "device", nil, "allow a host device path for container jobs (repeatable; needs --room)")
	cmd.Flags().StringArrayVar(&unmountEdits, "unmount", nil, "remove a named mount registration (repeatable; needs --room)")
	cmd.Flags().StringArrayVar(&undeviceEdits, "undevice", nil, "remove a device from the room's allow list (repeatable; needs --room)")
	cmd.Flags().StringVar(&registrationRoom, "room", "", "room that owns the --mount or --device registration")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive("allow-host-checks", "disallow-host-checks")
	cmd.MarkFlagsMutuallyExclusive("mount", "unmount")
	cmd.MarkFlagsMutuallyExclusive("device", "undevice")
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
		Short: "Remove a node; this node cannot be removed",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// The engine removes the node, as it owns every other change to one.
			// The command names the node it read by ID, because a name can equal
			// another node's ID and the engine resolves IDs first.
			m, err := store().Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			if _, err := saveMachine(c.Context(), m.ID, websvc.MachinePatch{Remove: true}); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Removed node %s.\n", m.Name)
			return nil
		},
	}
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
// IDs, so a renamed room keeps taking the node's work and a typo cannot limit
// a node to a room that does not exist.
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

// roomList words a node's room limit: the names of the rooms it takes work
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

// labelList words a label list for show: labels a node carries, or none.
func labelList(labels []string) string {
	if len(labels) == 0 {
		return "none (local CI needs the ci label)"
	}
	return strings.Join(labels, ", ")
}

// editLabels adds and removes owner labels, sorted and free of repeats.
func editLabels(current, add, remove []string) ([]string, error) {
	for _, name := range add {
		if _, err := machines.ParseLabel(strings.TrimSpace(name)); err != nil {
			return nil, fmt.Errorf("label %s: %w", name, err)
		}
	}
	out := make([]string, 0, len(current)+len(add))
	for _, label := range slices.Concat(current, add) {
		label = strings.TrimSpace(label)
		if !slices.Contains(remove, label) && label != "" && !slices.Contains(out, label) {
			out = append(out, label)
		}
	}
	slices.Sort(out)
	return out, nil
}

// orNone prints a value, or a dash when the probe read none.
func orNone(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
