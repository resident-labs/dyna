// Package machines keeps the one registry of machines that Dyna can use: the
// engine host, marked local, and every other machine it reaches over SSH. A
// machine that is enabled and whose room list allows a room takes that room's
// local CI work and jobs; the node's admission queue bounds how much runs.
package machines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Topic is the hub topic of a committed machine change. The engine wakes the
// CI workers on it, and clients see it.
const Topic = "machines"

// ResourceName identifies one counted resource or one-unit lock on a node.
type ResourceName string

var resourceNamePattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
var labelPattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}(?:=[a-z0-9._-]{1,64})?$`)

// ParseResourceName validates a resource name at an external boundary.
func ParseResourceName(name string) (ResourceName, error) {
	if !resourceNamePattern.MatchString(name) {
		return "", fmt.Errorf("resource name %q must use lowercase letters, digits, '-', '_' or '.', and be at most 64 characters", name)
	}
	return ResourceName(name), nil
}

// ParseResourceNames validates and deduplicates a job's resource requests.
func ParseResourceNames(names []string) ([]ResourceName, error) {
	var out []ResourceName
	seen := make(map[ResourceName]bool, len(names))
	for _, name := range names {
		parsed, err := ParseResourceName(name)
		if err != nil {
			return nil, err
		}
		if !seen[parsed] {
			seen[parsed] = true
			out = append(out, parsed)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ParseLabel validates one owner or placement label. A label is a name such as
// gpu, or a key-value pair such as arch=x86_64.
func ParseLabel(label string) (string, error) {
	if !labelPattern.MatchString(label) {
		return "", fmt.Errorf("label %q must use lowercase letters, digits, '-', '_' or '.', with an optional '=value'", label)
	}
	return label, nil
}

// ParseLabels validates, deduplicates and sorts labels from an external boundary.
func ParseLabels(labels []string) ([]string, error) {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		parsed, err := ParseLabel(label)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, parsed) {
			out = append(out, parsed)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Machine is one row of the registry.
type Machine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	SSH  string `json:"ssh"`
	// MaxIOHeavy caps how many IO-heavy jobs and checks the node runs at once.
	// Zero reads the default of one per sixteen probed CPUs, at least one.
	MaxIOHeavy int `json:"maxIOHeavy"`
	QuickSlots int `json:"quickSlots"`
	// MaxDiskPercent is the used-disk percent above which the node admits no
	// new work. Zero keeps a free-space floor instead; see DiskBlock.
	MaxDiskPercent int `json:"maxDiskPercent"`
	// Resources declares named counts available to jobs on this node. A name
	// that is not declared acts as a one-unit lock when a job requests it.
	Resources map[ResourceName]int `json:"resources,omitempty"`
	// Mounts and Devices are the container resources registered for each room.
	// Job submitters select them by name or path, never by a mount host source.
	Mounts  []NamedMount  `json:"mounts,omitempty"`
	Devices []DeviceGrant `json:"devices,omitempty"`
	// HostChecksGrants lists who allows repository-defined CI commands to run
	// on this machine outside a container: the owner for every room, or the
	// root of one room for that room's CI work. It is empty unless someone
	// allows them.
	HostChecksGrants []HostChecksGrant `json:"hostChecksGrants,omitempty"`
	// Rooms limits the CI work this machine takes to those room IDs. An empty
	// list allows every room, which is what every machine allowed before it
	// could name rooms.
	Rooms []string `json:"rooms,omitempty"`
	// Labels are the owner's stored placement labels. The ci label says the
	// machine runs local CI checks; an unlabeled machine still takes jobs when
	// enabled. Probed labels come from Capabilities instead of this list.
	Labels []string `json:"labels,omitempty"`
	// ContainerSocket is the container runtime socket a workflow job uses on
	// this machine, as a DOCKER_HOST value. It is empty for Docker's socket, so
	// a rootless Podman machine can name its own without changing anything else.
	ContainerSocket string `json:"containerSocket,omitempty"`
	// SSHConnectTimeout is how long an ssh to this machine waits for the
	// connection. Zero means DefaultSSHConnectTimeout. A machine reached through
	// a slow jump host needs a longer wait than a fixed one allows.
	SSHConnectTimeout time.Duration `json:"sshConnectTimeoutNs,omitempty"`
	// CheckTmp is the folder on this machine that holds each host check's own
	// temporary folder. It is empty for the machine's temporary directory
	// (TMPDIR, else /tmp). A machine whose /var/tmp is slow storage, such as
	// ZFS, gets a faster one here.
	CheckTmp string `json:"checkTmp,omitempty"`
	// Enabled turns the machine's work off. A disabled machine takes no new CI
	// runs. On the local row it stops that work, not the engine, which keeps
	// running.
	Enabled bool      `json:"enabled"`
	Local   bool      `json:"local"`
	Created time.Time `json:"createdAt"`
	Updated time.Time `json:"updatedAt"`

	// The probe fields are what the last probe read. A machine that has never
	// been probed has Health unknown and no capabilities.
	Health       Health       `json:"health"`
	Capabilities Capabilities `json:"capabilities"`
	Load         Load         `json:"load"`
	// LastProbe is when the last probe started, and LastSeen when a probe last
	// reached the machine. Both are zero before the first probe.
	LastProbe  time.Time `json:"lastProbe"`
	LastSeen   time.Time `json:"lastSeen"`
	ProbeError string    `json:"probeError"`
}

// Grantor is who allowed host checks on a machine.
type Grantor string

const (
	// GrantorOwner means the owner allowed them, for every room.
	GrantorOwner Grantor = "owner"
	// GrantorRoot means the root of one room allowed them for that room.
	GrantorRoot Grantor = "root"
)

// HostChecksGrant is one permission to run repository-defined CI commands
// outside a container on a machine.
type HostChecksGrant struct {
	// By names who allowed it: the owner, or the root of Room.
	By Grantor `json:"by"`
	// Room is the room a root allowed it for. It is empty for the owner, whose
	// grant covers every room.
	Room string `json:"room,omitempty"`
}

// DefaultContainerSocket is the Docker socket a machine uses when it names no
// container runtime of its own.
const DefaultContainerSocket = "unix:///var/run/docker.sock"

// DefaultSSHConnectTimeout is the ssh connect wait of a machine that names no
// timeout of its own.
const DefaultSSHConnectTimeout = 10 * time.Second

// MaxSSHConnectTimeout bounds a stored connect timeout, so a typo cannot hold a
// CI slot for hours.
const MaxSSHConnectTimeout = 10 * time.Minute

// ConnectTimeout is how long an ssh to this machine waits for the connection.
func (m Machine) ConnectTimeout() time.Duration {
	return ConnectTimeout(m.SSHConnectTimeout)
}

// ConnectTimeout reads a stored connect timeout: zero means the default.
func ConnectTimeout(stored time.Duration) time.Duration {
	if stored <= 0 {
		return DefaultSSHConnectTimeout
	}
	return stored
}

// SSHConnectTimeoutOption is the ssh -o value for a connect timeout. ssh reads
// whole seconds, so a shorter wait rounds up to one second.
func SSHConnectTimeoutOption(timeout time.Duration) string {
	seconds := int((ConnectTimeout(timeout) + time.Second - 1) / time.Second)
	return "ConnectTimeout=" + strconv.Itoa(seconds)
}

// DockerHost is the DOCKER_HOST value this machine's workflow jobs use.
func (m Machine) DockerHost() string {
	if m.ContainerSocket != "" {
		return m.ContainerSocket
	}
	return DefaultContainerSocket
}

// HostChecksAllowed reports whether host checks run on this machine for the
// CI work of a room. An owner's grant covers every room, including work that
// names no room; a root's grant covers its own room only, so one room's root
// cannot allow another room's commands to leave their container.
func HostChecksAllowed(grants []HostChecksGrant, room string) bool {
	for _, grant := range grants {
		if grant.By == GrantorOwner || (grant.Room != "" && grant.Room == room) {
			return true
		}
	}
	return false
}

// HostChecksAllowed reports whether this machine runs host checks for the CI
// work of a room.
func (m Machine) HostChecksAllowed(room string) bool {
	return HostChecksAllowed(m.HostChecksGrants, room)
}

// QuickSlotLimit bounds jobs in the quick lane.
func (m Machine) QuickSlotLimit() int {
	if m.QuickSlots > 0 {
		if m.Capabilities.MemTotal > 0 {
			return min(m.QuickSlots, max(1, int(m.Capabilities.MemTotal/(2<<30))))
		}
		return m.QuickSlots
	}
	limit := max(4, m.Capabilities.CPUs/4)
	if m.Capabilities.MemTotal > 0 {
		limit = max(1, min(limit, int(m.Capabilities.MemTotal/(8<<30))))
	}
	return limit
}

// IOHeavyLimit is how many IO-heavy jobs and checks this node runs at once.
// Zero reads the default: one per sixteen probed CPUs, at least one.
func (m Machine) IOHeavyLimit() int {
	if m.MaxIOHeavy > 0 {
		return m.MaxIOHeavy
	}
	return max(1, m.Capabilities.CPUs/16)
}

// DiskBlock says why the node's disk admits no new work, and is empty when it
// admits it. A set MaxDiskPercent is a limit on the used percent. Without one,
// the node keeps at least the larger of DiskFloorMin and DiskFloorShare of its
// disk free, so a large disk is not called full while it still has plenty of
// room. A node that was never probed is not blocked.
func (m Machine) DiskBlock() string {
	return diskBlock(m.MaxDiskPercent, m.Capabilities.Disk)
}

// diskBlock is DiskBlock for one disk reading. KeepFreeBytes is the
// boundary it judges: the node blocks while its free space is under it.
func diskBlock(maxPercent int, disk Disk) string {
	if disk.Total == 0 {
		return ""
	}
	if keep := KeepFreeBytes(maxPercent, disk.Total); disk.Free < keep {
		if maxPercent > 0 {
			used := int(100 * (disk.Total - disk.Free) / disk.Total)
			return fmt.Sprintf("disk %d%% at the %d%% limit", used, maxPercent)
		}
		return fmt.Sprintf("disk has %d GiB free, below the %d GiB floor", disk.Free>>30, keep>>30)
	}
	return ""
}

// DiskKeeps words the free space the node's disk rule keeps while it admits
// work: the floor for a node with no percent limit, or the percent limit. It
// is empty for a node that was never probed.
func (m Machine) DiskKeeps() string {
	disk := m.Capabilities.Disk
	if disk.Total == 0 {
		return ""
	}
	if m.MaxDiskPercent > 0 {
		return fmt.Sprintf("used under %d%%", m.MaxDiskPercent)
	}
	switch floor := diskFloorBytes(disk.Total); floor {
	case DiskFloorMin:
		return fmt.Sprintf("%d GiB and %d%% of the disk", DiskFloorMin>>30, DiskFloorShare)
	case disk.Total / 100 * DiskFloorShare:
		return fmt.Sprintf("%d%% of the disk", DiskFloorShare)
	default:
		return "a tenth of the disk"
	}
}

// diskFloorBytes is the free space a node with no percent limit keeps: the
// larger of DiskFloorMin and DiskFloorShare of its disk, never over a tenth of
// it. It is zero for a disk that was never probed.
func diskFloorBytes(total uint64) uint64 {
	return min(max(DiskFloorMin, total/100*DiskFloorShare), total/DiskFloorCapDivisor)
}

// DiskFloorMin and DiskFloorShare are the free space a node with no percent
// limit keeps: at least 20 GiB, and at least 5% of the disk. The floor never
// exceeds a tenth of the disk (DiskFloorCapDivisor), so a disk under 200 GiB
// keeps 10% free and an empty small disk always admits work.
const (
	DiskFloorMin        = uint64(20) << 30
	DiskFloorShare      = 5
	DiskFloorCapDivisor = 10
)

// KeepFreeBytes is the least free space the node's disk rule keeps for a
// disk of this size: with a percent limit, the free space that holds the used
// percent under it; without one, the floor DiskBlock judges. It is zero for a
// disk that was never probed. Cache recovery cleans to this boundary, so a
// node whose caches and scratch reach it admits work again.
func KeepFreeBytes(maxPercent int, total uint64) uint64 {
	if total == 0 {
		return 0
	}
	if maxPercent > 0 {
		// One byte over the limit's share holds the whole-point used percent
		// under the limit.
		return total - total*uint64(maxPercent)/100 + 1
	}
	return diskFloorBytes(total)
}

// HasHostChecksGrants reports whether anyone allows host checks on this
// machine, whatever the room.
func (m Machine) HasHostChecksGrants() bool { return len(m.HostChecksGrants) > 0 }

// uniqueGrants drops a repeated grant and sorts the rest, so a stored grant
// list never says the same thing twice.
func uniqueGrants(grants []HostChecksGrant) []HostChecksGrant {
	out := make([]HostChecksGrant, 0, len(grants))
	for _, grant := range grants {
		if !slices.Contains(out, grant) {
			out = append(out, grant)
		}
	}
	slices.SortFunc(out, func(a, b HostChecksGrant) int {
		if a.By != b.By {
			return strings.Compare(string(a.By), string(b.By))
		}
		return strings.Compare(a.Room, b.Room)
	})
	return out
}

// AllowsRoom reports whether the machine takes CI work for the room. A machine
// that names no room takes work from every room.
func (m Machine) AllowsRoom(roomID string) bool {
	return len(m.Rooms) == 0 || slices.Contains(m.Rooms, roomID)
}

// LabelCI marks a machine that takes local CI work. Jobs use any enabled
// machine, so only local CI checks read this label.
const LabelCI = "ci"

// ProbedLabels derives labels from the last good probe. They stay separate
// from owner labels and change only when a probe stores new capabilities.
func (m Machine) ProbedLabels() []string {
	caps := m.Capabilities
	labels := make([]string, 0, 5)
	if caps.OS != "" {
		labels = append(labels, "os="+strings.ToLower(caps.OS))
	}
	if caps.Arch != "" {
		labels = append(labels, "arch="+strings.ToLower(caps.Arch))
	}
	if caps.GPU {
		labels = append(labels, "gpu")
	}
	for _, runtime := range caps.Containers.Names() {
		labels = append(labels, runtime)
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// HasLabel reports whether the owner or the last probe gave the machine a label.
func (m Machine) HasLabel(label string) bool {
	return slices.Contains(m.Labels, label) || slices.Contains(m.ProbedLabels(), label)
}

// ErrLocal marks a change that would remove or unmake the engine host.
var ErrLocal = errors.New("this node runs the engine")

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var sshTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*(@[a-zA-Z0-9][a-zA-Z0-9_.-]*)?$`)
var invalidName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// ValidateName reports whether name can name a machine.
func ValidateName(name string) error {
	if !nodeName.MatchString(name) {
		return errors.New("node name must contain only letters, numbers, dots, underscores or hyphens, and start with a letter or number")
	}
	return nil
}

// ValidateSSH reports whether target is a host or user@host, with no shell
// syntax that a command line could misuse.
func ValidateSSH(target string) error {
	if !sshTarget.MatchString(target) {
		return errors.New("SSH destination must be a host or user@host")
	}
	return nil
}

// Validate reports whether the machine can be stored as it stands.
func (m Machine) Validate() error {
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	if m.Local && m.SSH != "" {
		return errors.New("this node needs no SSH destination")
	}
	if !m.Local && m.SSH == "" {
		return errors.New("a node other than this one needs an SSH destination")
	}
	if m.SSH != "" {
		if err := ValidateSSH(m.SSH); err != nil {
			return err
		}
	}
	if m.SSHConnectTimeout < 0 || m.SSHConnectTimeout > MaxSSHConnectTimeout {
		return fmt.Errorf("SSH connect timeout must not be negative or longer than %s", MaxSSHConnectTimeout)
	}
	if m.CheckTmp != "" && (!path.IsAbs(m.CheckTmp) || path.Clean(m.CheckTmp) != m.CheckTmp || m.CheckTmp == "/" || strings.ContainsRune(m.CheckTmp, 0)) {
		return errors.New("the check temporary folder must be a clean absolute path other than /")
	}
	if m.QuickSlots < 0 || m.QuickSlots > 1024 {
		return fmt.Errorf("quick slots must be between 0 and 1024")
	}
	if m.MaxIOHeavy < 0 || m.MaxIOHeavy > 1024 {
		return errors.New("the IO-heavy limit must be between 0, the default, and 1024")
	}
	if m.MaxDiskPercent < 0 || m.MaxDiskPercent > 100 {
		return errors.New("the disk limit percent must be between 0, the default, and 100")
	}
	for name, count := range m.Resources {
		if _, err := ParseResourceName(string(name)); err != nil {
			return err
		}
		if count <= 0 {
			return fmt.Errorf("resource %s count must be positive", name)
		}
	}
	if err := validateRoomList(m.Rooms); err != nil {
		return err
	}
	if err := validateLabelList(m.Labels); err != nil {
		return err
	}
	if err := validateGrants(m.HostChecksGrants); err != nil {
		return err
	}
	return validateContainerRegistrations(m.Mounts, m.Devices)
}

// validateLabelList reports whether a label list is storable. Label names
// follow the resource name rule, and each label appears once.
func validateLabelList(labels []string) error {
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		if _, err := ParseLabel(label); err != nil {
			return err
		}
		if seen[label] {
			return fmt.Errorf("label %s appears twice", label)
		}
		seen[label] = true
	}
	return nil
}

// validateRoomList reports whether a room limit is storable.
func validateRoomList(rooms []string) error {
	seen := make(map[string]bool, len(rooms))
	for _, room := range rooms {
		if strings.TrimSpace(room) != room || room == "" || len(room) > 64 || strings.ContainsAny(room, " ,\t\r\n") {
			return fmt.Errorf("room %q must be a room ID of at most 64 bytes", room)
		}
		if seen[room] {
			return fmt.Errorf("room %q is named twice", room)
		}
		seen[room] = true
	}
	return nil
}

// validateGrants reports whether every host-check grant names the owner, or the
// root of one room.
func validateGrants(grants []HostChecksGrant) error {
	for _, grant := range grants {
		switch {
		case grant.By == GrantorOwner && grant.Room == "":
		case grant.By == GrantorRoot && grant.Room != "":
		default:
			return fmt.Errorf("a host-check grant needs the owner, or the root of one room, not %q in %q", grant.By, grant.Room)
		}
	}
	return nil
}

const schemaVersion = 18

const schema = `CREATE TABLE machines (
	id             TEXT    PRIMARY KEY,
	name           TEXT    NOT NULL UNIQUE,
	ssh            TEXT    NOT NULL,
	enabled        INTEGER NOT NULL CHECK(enabled IN (0,1)),
	local          INTEGER NOT NULL CHECK(local IN (0,1)),
	created_ms     INTEGER NOT NULL,
	updated_ms     INTEGER NOT NULL,
	capabilities   TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(capabilities)),
	health         TEXT    NOT NULL DEFAULT 'unknown',
	last_probe_ms  INTEGER NOT NULL DEFAULT 0,
	last_seen_ms   INTEGER NOT NULL DEFAULT 0,
	probe_error    TEXT    NOT NULL DEFAULT '',
	last_load      TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(last_load)),
	host_checks_grants   TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(host_checks_grants)),
	rooms          TEXT    NOT NULL DEFAULT '[]' CHECK(json_valid(rooms)),
	labels         TEXT    NOT NULL DEFAULT '[]' CHECK(json_valid(labels)),
	container_socket TEXT  NOT NULL DEFAULT '',
	ssh_connect_timeout_ms INTEGER NOT NULL DEFAULT 0 CHECK(ssh_connect_timeout_ms >= 0),
	check_tmp TEXT NOT NULL DEFAULT '',
	max_io_heavy INTEGER NOT NULL DEFAULT 0 CHECK(max_io_heavy >= 0),
	quick_slots INTEGER NOT NULL DEFAULT 0 CHECK(quick_slots >= 0),
	max_disk_percent INTEGER NOT NULL DEFAULT 0 CHECK(max_disk_percent BETWEEN 0 AND 100),
	resources TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(resources)),
	mounts TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(mounts)),
	devices TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(devices))
);

CREATE TABLE IF NOT EXISTS machine_setting_changes (node_id TEXT NOT NULL, changed_ms INTEGER NOT NULL, actor_role TEXT NOT NULL, actor_session TEXT NOT NULL, actor_room TEXT NOT NULL, field TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS machine_setting_changes_node ON machine_setting_changes(node_id, changed_ms);
CREATE UNIQUE INDEX machines_one_local ON machines(local) WHERE local = 1;`

// probeColumns add what the probes read to a version 1 table. A row that was
// stored before the first probe keeps its registry data and starts unknown.
var probeColumns = []string{
	`ALTER TABLE machines ADD COLUMN capabilities TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(capabilities))`,
	`ALTER TABLE machines ADD COLUMN health TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE machines ADD COLUMN last_probe_ms INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE machines ADD COLUMN last_seen_ms INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE machines ADD COLUMN probe_error TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE machines ADD COLUMN last_load TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(last_load))`,
}

// grantColumns record who allows host checks, and for which room, on a table
// that kept that permission as one boolean. Before this version the owner was
// the only one who could allow it, so a permission that is on becomes the
// owner's grant for every room.
var grantColumns = []string{
	`ALTER TABLE machines ADD COLUMN host_checks_grants TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(host_checks_grants))`,
	`UPDATE machines SET host_checks_grants='[{"by":"owner"}]' WHERE allow_host_checks=1`,
	`ALTER TABLE machines DROP COLUMN allow_host_checks`,
}

// degradedColumns add the reason of a degraded health to a version 2 table.
// A row stored before the reason existed is online or offline, so it needs
// none.
var degradedColumns = []string{
	`ALTER TABLE machines ADD COLUMN health_reason TEXT NOT NULL DEFAULT ''`,
}

// registryColumns are the columns a machine edit writes. The probe columns
// change only through SaveProbe.
const registryColumns = `id,name,ssh,enabled,local,created_ms,updated_ms`

const columns = registryColumns + `,capabilities,health,last_probe_ms,last_seen_ms,probe_error,last_load,host_checks_grants,rooms,labels,container_socket,ssh_connect_timeout_ms,check_tmp,max_io_heavy,max_disk_percent,resources,mounts,devices,quick_slots`

// Store keeps the machines registry in dyna.db.
type Store struct{ DataDir string }

// Migrate prepares the machines table, adds the engine host row when the
// registry has none, and imports the old local CI nodes. Only the engine calls
// it, at start, after the local CI store has migrated.
func Migrate(dataDir string) error {
	_, err := Store{DataDir: dataDir}.db()
	return err
}

func (s Store) root() string {
	if s.DataDir != "" {
		return s.DataDir
	}
	return store.DataDir()
}

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.root())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "machines.version", schemaVersion, func(conn *sql.Conn, old int) error {
		return prepare(conn, old)
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// prepare runs inside the migration. Version 1 created the registry and
// imported the old local CI nodes. Later versions added probe data, degradation
// reasons, the host-check permission as one switch, who allows host checks for
// which room, and dropped the degradation reason when the one disk rule
// replaced it.
func prepare(conn *sql.Conn, old int) error {
	ctx := context.Background()
	if old >= schemaVersion {
		return nil
	}
	if old == 0 {
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		if err := addLocal(ctx, conn); err != nil {
			return err
		}
		return importLocalCINodes(ctx, conn)
	}
	if old < 18 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN quick_slots INTEGER NOT NULL DEFAULT 0 CHECK(quick_slots >= 0)`); err != nil {
			return err
		}
	}
	if old == 1 {
		if err := addProbeColumns(ctx, conn); err != nil {
			return err
		}
	}
	if old < 3 {
		if err := addDegradedColumn(ctx, conn); err != nil {
			return err
		}
	}
	if old < 4 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN allow_host_checks INTEGER NOT NULL DEFAULT 0 CHECK(allow_host_checks IN (0,1))`); err != nil {
			return err
		}
		if err := inheritHostChecks(ctx, conn); err != nil {
			return err
		}
	}
	if old < 5 {
		// A machine with no room list takes work from every room, which is
		// what every machine allowed before it could name rooms.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN rooms TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(rooms))`); err != nil {
			return err
		}
	}
	if old < 6 {
		for _, statement := range grantColumns {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("record who allows host checks: %w", err)
			}
		}
	}
	if old < 8 {
		// A machine without its own runtime socket uses Docker's, which is what
		// every machine allowed before it could name one.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN container_socket TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if old < 9 {
		// A machine with no stored timeout waits the default, which is what every
		// machine waited before it could name one.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN ssh_connect_timeout_ms INTEGER NOT NULL DEFAULT 0 CHECK(ssh_connect_timeout_ms >= 0)`); err != nil {
			return err
		}
	}
	if old < 10 {
		// A machine with no folder uses the node's own temporary directory, which
		// is where a host check's temporary folder should have been all along.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN check_tmp TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if old < 7 {
		// A machine with no suite list takes every suite, which is what every
		// machine allowed before it could name suites.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN suites TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(suites))`); err != nil {
			return err
		}
	}
	if old < 12 {
		// A machine with no declared resources still accepts one-unit named locks.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN resources TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(resources))`); err != nil {
			return err
		}
	}
	if old < 11 {
		// A machine with no stored node limit reads the default of its limits:
		// one IO-heavy job per sixteen probed CPUs, at least one, and 90
		// percent of the disk in use.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN max_io_heavy INTEGER NOT NULL DEFAULT 0 CHECK(max_io_heavy >= 0)`); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN max_disk_percent INTEGER NOT NULL DEFAULT 0 CHECK(max_disk_percent BETWEEN 0 AND 100)`); err != nil {
			return err
		}
	}
	if old < 13 {
		// The one disk rule dropped the reason a degraded health carried: the
		// queue's own words say why a disk admits no work.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines DROP COLUMN health_reason`); err != nil {
			return err
		}
	}
	if old < 14 {
		// A node no longer has roles or CI slots. The one choice a role made
		// survives as a label: a machine the owner gave the ci role keeps
		// taking local CI, and every other machine stays out of it. The
		// admission queue alone bounds how much runs.
		if _, err := conn.ExecContext(ctx, `ALTER TABLE machines ADD COLUMN labels TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(labels))`); err != nil {
			return fmt.Errorf("add machines.labels: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE machines SET labels='["ci"]' WHERE roles LIKE ?`, `%"ci"%`); err != nil {
			return fmt.Errorf("carry the ci role into labels: %w", err)
		}
		for _, column := range []string{"roles", "ci_slots"} {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE machines DROP COLUMN "+column); err != nil {
				return fmt.Errorf("drop machines.%s: %w", column, err)
			}
		}
	}
	if old < 16 {
		// Jobs could not use node-registered mounts or devices before this version.
		for _, column := range []string{"mounts", "devices"} {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE machines ADD COLUMN "+column+" TEXT NOT NULL DEFAULT '[]' CHECK(json_valid("+column+"))"); err != nil {
				return fmt.Errorf("add machines.%s: %w", column, err)
			}
		}
	}
	if old < 17 {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS machine_setting_changes (node_id TEXT NOT NULL, changed_ms INTEGER NOT NULL, actor_role TEXT NOT NULL, actor_session TEXT NOT NULL, actor_room TEXT NOT NULL, field TEXT NOT NULL); CREATE INDEX IF NOT EXISTS machine_setting_changes_node ON machine_setting_changes(node_id, changed_ms)`); err != nil {
			return err
		}
	}
	if old < 15 {
		// A suite list becomes owner labels before its column goes. A machine
		// with no suite= labels remains unrestricted, exactly as an empty suite
		// list was; a machine with them takes only the suites they name.
		if err := importSuiteLabels(ctx, conn); err != nil {
			return fmt.Errorf("import node suite labels: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "ALTER TABLE machines DROP COLUMN suites"); err != nil {
			return fmt.Errorf("drop machines.suites: %w", err)
		}
	}
	return nil
}

// importSuiteLabels carries each old suite filter into owner labels once.
func importSuiteLabels(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "SELECT id, labels, suites FROM machines WHERE json_array_length(suites) > 0")
	if err != nil {
		return err
	}
	type update struct{ id, labels string }
	var updates []update
	for rows.Next() {
		var id, rawLabels, rawSuites string
		if err := rows.Scan(&id, &rawLabels, &rawSuites); err != nil {
			rows.Close()
			return err
		}
		var labels, suites []string
		if err := json.Unmarshal([]byte(rawLabels), &labels); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(rawSuites), &suites); err != nil {
			rows.Close()
			return err
		}
		for _, suite := range suites {
			label := "suite=" + suite
			if !slices.Contains(labels, label) {
				labels = append(labels, label)
			}
		}
		raw, err := json.Marshal(labels)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id: id, labels: string(raw)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := conn.ExecContext(ctx, "UPDATE machines SET labels=? WHERE id=?", update.labels, update.id); err != nil {
			return err
		}
	}
	return nil
}

// inheritHostChecks is a one-time migration for machines with proof that the
// old host-based computer-smoke command started. New machines and machines with
// only setup evidence stay opted out. A repository file cannot grant permission.
// TODO: legacy/shim remove when pre-repository-definition local_ci_results are
// no longer retained; after that, every machine uses only its explicit setting.
func inheritHostChecks(ctx context.Context, conn *sql.Conn) error {
	present, err := database.HasTable(ctx, conn, "local_ci_results")
	if err != nil || !present {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE machines SET allow_host_checks=1
WHERE EXISTS (
 SELECT 1 FROM local_ci_results AS results, json_each(results.payload,'$.suites') AS item
 WHERE json_extract(item.value,'$.suite')='computer-smoke'
 AND json_extract(item.value,'$.node')=machines.name
 AND (json_extract(item.value,'$.status')='passed'
  OR (json_extract(item.value,'$.status')='failed' AND json_extract(item.value,'$.cause')='test-failure'))
) OR EXISTS (
 SELECT 1 FROM local_ci_results AS results, json_each(results.payload,'$.checks') AS item
 WHERE json_extract(item.value,'$.suite')='computer-smoke'
 AND json_extract(item.value,'$.node')=machines.name
 AND (json_extract(item.value,'$.status')='passed'
  OR (json_extract(item.value,'$.status')='failed' AND json_extract(item.value,'$.cause')='test-failure'))
)`)
	return err
}

// addDegradedColumn adds the reason of a degraded health to a version 2 table.
func addDegradedColumn(ctx context.Context, conn *sql.Conn) error {
	for _, statement := range degradedColumns {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add degradation column: %w", err)
		}
	}
	return nil
}

// addProbeColumns adds the probe columns to a version 1 table.
func addProbeColumns(ctx context.Context, conn *sql.Conn) error {
	for _, statement := range probeColumns {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add probe columns: %w", err)
		}
	}
	return nil
}

// addLocal inserts the engine host row when the registry has none. The name is
// this machine's host name, reduced to the characters a name may hold.
func addLocal(ctx context.Context, conn *sql.Conn) error {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE local=1`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	name, err := freeName(ctx, conn, localName())
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err = conn.ExecContext(ctx, `INSERT INTO machines(`+registryColumns+`,labels) VALUES(?,?,'',1,1,?,?,'[]')`,
		newID(), name, now, now)
	return err
}

// localName is this machine's host name as a machine name.
func localName() string {
	host, err := os.Hostname()
	if err != nil {
		return "this-machine"
	}
	if name := sanitizeName(host); name != "" {
		return name
	}
	return "this-machine"
}

// sanitizeName reduces s to the characters a machine name may hold.
func sanitizeName(s string) string {
	return strings.TrimLeft(invalidName.ReplaceAllString(strings.TrimSpace(s), "-"), "-._")
}

// freeName returns base, or base with a counter, so it names no other machine.
func freeName(ctx context.Context, conn *sql.Conn, base string) (string, error) {
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE name=?`, name).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return name, nil
		}
	}
}

// importLocalCINodes turns the old local CI node rows into machines and drops
// their table. A node with no SSH destination ran on this machine, so it needs
// no row: the engine host already takes that work.
//
// TODO: legacy/shim remove later — imports local_ci_nodes into machines
func importLocalCINodes(ctx context.Context, conn *sql.Conn) error {
	present, err := database.HasTable(ctx, conn, "local_ci_nodes")
	if err != nil || !present {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT name,ssh,enabled FROM local_ci_nodes ORDER BY name`)
	if err != nil {
		return err
	}
	type oldNode struct {
		name, ssh string
		enabled   bool
	}
	var old []oldNode
	for rows.Next() {
		var n oldNode
		if err := rows.Scan(&n.name, &n.ssh, &n.enabled); err != nil {
			rows.Close()
			return err
		}
		old = append(old, n)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, n := range old {
		if err := importNode(ctx, conn, n.name, n.ssh, n.enabled); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `DROP TABLE local_ci_nodes`)
	return err
}

// importNode stores one old CI node row as a machine. The old node's switch
// said whether its work ran, and it becomes the machine's switch.
func importNode(ctx context.Context, conn *sql.Conn, name, ssh string, enabled bool) error {
	if ssh == "" {
		// A local node ran its CI work on the engine host. An enabled node keeps
		// that work, so the engine host row carries the ci label; a disabled one
		// leaves the row opted out.
		if !enabled {
			return nil
		}
		_, err := conn.ExecContext(ctx, `UPDATE machines SET labels=json_insert(labels, '$[#]', 'ci')
			WHERE local=1 AND NOT EXISTS (SELECT 1 FROM json_each(labels) WHERE value='ci')`)
		return err
	}
	now := time.Now().UnixMilli()
	name, err := freeName(ctx, conn, sanitizeName(name))
	if err != nil {
		return err
	}
	// An old CI node keeps taking local CI, so its label says so.
	_, err = conn.ExecContext(ctx, `INSERT INTO machines(`+registryColumns+`,labels) VALUES(?,?,?,?,0,?,?,'["ci"]')`,
		newID(), name, ssh, enabled, now, now)
	return err
}

// List returns every machine, the engine host first and the rest by name.
func (s Store) List(ctx context.Context) ([]Machine, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM machines ORDER BY local DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Machine{}
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// Get returns the machine with this name, or os.ErrNotExist.
func (s Store) Get(ctx context.Context, name string) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	return getOn(ctx, db, name, false)
}

// GetLocal returns the row of the machine that runs this engine.
func (s Store) GetLocal(ctx context.Context) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	return getOn(ctx, db, "", true)
}

// GetByID returns the machine with this ID, or os.ErrNotExist. A name can
// change, so a caller that holds an ID reads the row it just changed.
func (s Store) GetByID(ctx context.Context, id string) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	return getByID(ctx, db, id)
}

// querier runs the read that finds one machine.
type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// getOn reads one machine. An empty name with local reads the engine host row.
func getOn(ctx context.Context, q querier, name string, local bool) (Machine, error) {
	where, arg := "name", any(name)
	if local {
		where, arg = "local", any(true)
	}
	m, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE `+where+`=?`, arg))
	if errors.Is(err, sql.ErrNoRows) {
		if local {
			return Machine{}, fmt.Errorf("this node is not in the registry: %w", os.ErrNotExist)
		}
		return Machine{}, fmt.Errorf("node %q: %w", name, os.ErrNotExist)
	}
	return m, err
}

// getByID reads the machine with this ID.
func getByID(ctx context.Context, q querier, id string) (Machine, error) {
	m, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, fmt.Errorf("node %s: %w", id, os.ErrNotExist)
	}
	return m, err
}

// scanner reads one machine row.
type scanner interface{ Scan(...any) error }

func scan(row scanner) (Machine, error) {
	var m Machine
	var capabilities, health, load, probeError, grants, rooms, labels, resources, mounts, devices string
	var created, updated, probed, seen, connectMs int64
	if err := row.Scan(&m.ID, &m.Name, &m.SSH, &m.Enabled, &m.Local, &created, &updated,
		&capabilities, &health, &probed, &seen, &probeError, &load, &grants, &rooms, &labels, &m.ContainerSocket, &connectMs, &m.CheckTmp, &m.MaxIOHeavy, &m.MaxDiskPercent, &resources, &mounts, &devices, &m.QuickSlots); err != nil {
		return Machine{}, err
	}
	m.SSHConnectTimeout = time.Duration(connectMs) * time.Millisecond
	if err := json.Unmarshal([]byte(resources), &m.Resources); err != nil {
		return Machine{}, fmt.Errorf("node %s resources: %w", m.Name, err)
	}
	if len(m.Resources) == 0 {
		m.Resources = nil
	}
	if err := json.Unmarshal([]byte(mounts), &m.Mounts); err != nil {
		return Machine{}, fmt.Errorf("node %s mounts: %w", m.Name, err)
	}
	if len(m.Mounts) == 0 {
		m.Mounts = nil
	}
	if err := json.Unmarshal([]byte(devices), &m.Devices); err != nil {
		return Machine{}, fmt.Errorf("node %s devices: %w", m.Name, err)
	}
	if len(m.Devices) == 0 {
		m.Devices = nil
	}
	if err := json.Unmarshal([]byte(grants), &m.HostChecksGrants); err != nil {
		return Machine{}, fmt.Errorf("node %s host-check grants: %w", m.Name, err)
	}
	if len(m.HostChecksGrants) == 0 {
		m.HostChecksGrants = nil
	}
	if err := json.Unmarshal([]byte(rooms), &m.Rooms); err != nil {
		return Machine{}, fmt.Errorf("node %s rooms: %w", m.Name, err)
	}
	// A machine that names no room reads back with no list, so every stored row
	// has one meaning of "every room".
	if len(m.Rooms) == 0 {
		m.Rooms = nil
	}
	if err := json.Unmarshal([]byte(labels), &m.Labels); err != nil {
		return Machine{}, fmt.Errorf("node %s labels: %w", m.Name, err)
	}
	// A machine that names no label reads back with no list, as rooms do.
	if len(m.Labels) == 0 {
		m.Labels = nil
	}
	if err := json.Unmarshal([]byte(capabilities), &m.Capabilities); err != nil {
		return Machine{}, fmt.Errorf("node %s capabilities: %w", m.Name, err)
	}
	if err := json.Unmarshal([]byte(load), &m.Load); err != nil {
		return Machine{}, fmt.Errorf("node %s load: %w", m.Name, err)
	}
	m.Health, m.ProbeError = parseHealth(health), probeError
	m.Created = time.UnixMilli(created)
	m.Updated = time.UnixMilli(updated)
	if probed > 0 {
		m.LastProbe = time.UnixMilli(probed)
	}
	if seen > 0 {
		m.LastSeen = time.UnixMilli(seen)
	}
	return m, nil
}

// healthOnToggle keeps a machine's health while it stays enabled, and shows
// disabled while it is turned off. A machine that comes back starts unknown,
// so no client reads the last probe of a machine that was off.
const healthOnToggle = `CASE WHEN ? = 0 THEN 'disabled' WHEN health = 'disabled' THEN 'unknown' ELSE health END`

// healthOnCreate is the health a new machine starts with.
func healthOnCreate(enabled bool) Health {
	if !enabled {
		return HealthDisabled
	}
	return HealthUnknown
}

// flag is the integer form of a switch that SQL compares with = 0.
func flag(on bool) int {
	if on {
		return 1
	}
	return 0
}

// SaveProbe stores what one probe read, and returns the stored row. A failed
// probe keeps the capabilities and the load of the last good probe, sets the
// machine offline and records the error. A machine that is off stores disabled
// and nothing else.
func (s Store) SaveProbe(ctx context.Context, id string, r ProbeResult) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	stored, err := getByID(ctx, db, id)
	if err != nil {
		return Machine{}, err
	}
	health := r.Health
	if r.At.IsZero() {
		r.At = time.Now()
	}
	// A machine the owner turned off holds nothing and shows nothing: it takes
	// no work either way. Its readings stay as the last good probe left them,
	// and enabling it again starts unknown, so this comes last.
	if !stored.Enabled {
		health = HealthDisabled
	}
	// Only a probe that reached the machine replaces what the last good probe
	// read, so a machine that is off keeps its capabilities for the owner.
	var capabilities, load string
	diskAfter := stored.Capabilities.Disk
	if health == HealthOnline {
		rawCaps, err := json.Marshal(r.Capabilities)
		if err != nil {
			return Machine{}, err
		}
		rawLoad, err := json.Marshal(r.Load)
		if err != nil {
			return Machine{}, err
		}
		capabilities, load = string(rawCaps), string(rawLoad)
		diskAfter = r.Capabilities.Disk
	}
	probeErr := ""
	if health == HealthOffline {
		probeErr = r.Err
	}
	probed := r.At.UnixMilli()
	// A probe that starts or ends a disk block changes where new work can run,
	// so it wakes the workers that queue it. Any other probe result changes no
	// work.
	key := probeKeyPrefix + stored.Name
	if diskBlock(stored.MaxDiskPercent, diskAfter) != diskBlock(stored.MaxDiskPercent, stored.Capabilities.Disk) {
		key = blockKeyPrefix + stored.Name
	}
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: key}, func(conn *sql.Conn) error {
		if health == HealthOnline {
			_, err := conn.ExecContext(ctx, `UPDATE machines SET capabilities=?, last_load=?, health=?, last_probe_ms=?, last_seen_ms=?, probe_error='' WHERE id=?`,
				capabilities, load, string(health), probed, probed, id)
			return err
		}
		if health == HealthDisabled {
			_, err := conn.ExecContext(ctx, `UPDATE machines SET health=?, last_probe_ms=?, probe_error='' WHERE id=?`,
				string(health), probed, id)
			return err
		}
		_, err := conn.ExecContext(ctx, `UPDATE machines SET health=?, last_probe_ms=?, probe_error=? WHERE id=?`,
			string(health), probed, probeErr, id)
		return err
	})
	if err != nil {
		return Machine{}, err
	}
	return getByID(ctx, db, id)
}

// Save inserts a machine, or updates the stored row that has this ID, and
// returns the stored row. An update keeps the row's ID, its created time and
// its local mark, so a caller can never add a second engine host or turn a
// stored machine into one. A new machine takes a name that no other row has.
func (s Store) Save(ctx context.Context, m Machine) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	existing, found, err := find(ctx, db, m)
	if err != nil {
		return Machine{}, err
	}
	now := time.Now()
	if found {
		m.ID, m.Local, m.Created = existing.ID, existing.Local, existing.Created
	} else {
		m.ID, m.Local, m.Created = newID(), false, now
	}
	m.HostChecksGrants = uniqueGrants(m.HostChecksGrants)
	m.Updated = now
	if err := m.Validate(); err != nil {
		return Machine{}, err
	}
	rooms, labels := marshalNames(m.Rooms), marshalNames(m.Labels)
	resources := marshalResources(m.Resources)
	mounts, devices := marshalContainerRegistrations(m.Mounts), marshalContainerRegistrations(m.Devices)
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + m.Name}, func(conn *sql.Conn) error {
		if found {
			// An unrelated edit keeps access grants. Update changes the complete
			// node from a fresh read in its own writer transaction.
			stored, err := getByID(ctx, conn, m.ID)
			if err != nil {
				return err
			}
			m.HostChecksGrants = stored.HostChecksGrants
			m.Rooms = stored.Rooms
			m.Mounts = stored.Mounts
			m.Devices = stored.Devices
			if err := recordMachineChanges(ctx, conn, stored, m); err != nil {
				return err
			}
			return updateMachine(ctx, conn, m)
		}
		extant, err := getOn(ctx, conn, m.Name, false)
		if err == nil && extant.ID != m.ID {
			return fmt.Errorf("node %q exists: %w", m.Name, os.ErrExist)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO machines(`+registryColumns+`,health,host_checks_grants,rooms,labels,container_socket,ssh_connect_timeout_ms,check_tmp,max_io_heavy,max_disk_percent,resources,mounts,devices,quick_slots) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.ID, m.Name, m.SSH, m.Enabled, m.Local,
			m.Created.UnixMilli(), m.Updated.UnixMilli(), string(healthOnCreate(m.Enabled)), marshalGrants(m.HostChecksGrants), rooms, labels, m.ContainerSocket, m.SSHConnectTimeout.Milliseconds(), m.CheckTmp, m.MaxIOHeavy, m.MaxDiskPercent, resources, mounts, devices, m.QuickSlots)
		if err != nil {
			return err
		}
		return recordMachineChanges(ctx, conn, Machine{}, m)
	})
	if err != nil {
		return Machine{}, err
	}
	return m, nil
}

// UpdateHostChecksGrants reads a machine's host-check grants inside the writer
// transaction, hands them to change, and stores what change returns. The read
// and the write share one transaction, so two callers cannot overwrite each
// other's grant and a change never restores a grant that another caller refused
// meanwhile. A change that returns an error leaves the row as it stood.
func (s Store) UpdateHostChecksGrants(ctx context.Context, id string, change func(Machine) ([]HostChecksGrant, error)) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	// The name names the event; only the grants need the transaction.
	stored, err := getByID(ctx, db, id)
	if err != nil {
		return Machine{}, err
	}
	var out Machine
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + stored.Name}, func(conn *sql.Conn) error {
		m, err := getByID(ctx, conn, id)
		if err != nil {
			return err
		}
		grants, err := change(m)
		if err != nil {
			return err
		}
		grants = uniqueGrants(grants)
		changed := m
		changed.HostChecksGrants = grants
		changed.Updated = time.Now()
		if err := recordMachineChanges(ctx, conn, m, changed); err != nil {
			return err
		}
		if err := validateGrants(grants); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE machines SET host_checks_grants=?, updated_ms=? WHERE id=?`,
			marshalGrants(grants), time.Now().UnixMilli(), id); err != nil {
			return err
		}
		out, err = getByID(ctx, conn, id)
		return err
	})
	if err != nil {
		return Machine{}, err
	}
	return out, nil
}

// Update reads a node inside the writer transaction, applies and validates the
// complete change, then commits every field together. A callback error or an
// invalid result leaves the stored node unchanged.
func (s Store) Update(ctx context.Context, id string, change func(*Machine) error) (Machine, error) {
	db, err := s.db()
	if err != nil {
		return Machine{}, err
	}
	stored, err := getByID(ctx, db, id)
	if err != nil {
		return Machine{}, err
	}
	var out Machine
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + stored.Name}, func(conn *sql.Conn) error {
		m, err := getByID(ctx, conn, id)
		if err != nil {
			return err
		}
		before := m
		before.Rooms = slices.Clone(m.Rooms)
		before.Labels = slices.Clone(m.Labels)
		before.HostChecksGrants = slices.Clone(m.HostChecksGrants)
		before.Mounts = slices.Clone(m.Mounts)
		before.Devices = slices.Clone(m.Devices)
		before.Resources = maps.Clone(m.Resources)
		created, local := m.Created, m.Local
		if err := change(&m); err != nil {
			return err
		}
		m.ID, m.Created, m.Local = id, created, local
		m.Updated = time.Now()
		m.HostChecksGrants = uniqueGrants(m.HostChecksGrants)
		if err := m.Validate(); err != nil {
			return err
		}
		if err := recordMachineChanges(ctx, conn, before, m); err != nil {
			return err
		}
		if err := updateMachine(ctx, conn, m); err != nil {
			return err
		}
		out, err = getByID(ctx, conn, id)
		return err
	})
	if err != nil {
		return Machine{}, err
	}
	return out, nil
}

// MachineField names a node setting in the change history.
type MachineField string

// FieldChange records the latest change to one node setting.
type FieldChange struct {
	Field   MachineField
	Changed time.Time
	Actor   authority.Actor
}

func recordMachineChanges(ctx context.Context, conn *sql.Conn, before, after Machine) error {
	// Probe samples have their own history and are not setting edits.
	fields := []struct {
		name    MachineField
		changed bool
	}{
		{"name", before.Name != after.Name}, {"ssh", before.SSH != after.SSH},
		{"enabled", before.Enabled != after.Enabled}, {"rooms", before.ID == "" || !slices.Equal(before.Rooms, after.Rooms)},
		{"labels", !slices.Equal(before.Labels, after.Labels)}, {"host checks", !slices.Equal(before.HostChecksGrants, after.HostChecksGrants)},
		{"container socket", before.ContainerSocket != after.ContainerSocket}, {"SSH timeout", before.SSHConnectTimeout != after.SSHConnectTimeout},
		{"check temporary folder", before.CheckTmp != after.CheckTmp}, {"IO limit", before.MaxIOHeavy != after.MaxIOHeavy}, {"quick slots", before.QuickSlots != after.QuickSlots},
		{"disk limit", before.MaxDiskPercent != after.MaxDiskPercent}, {"resources", !maps.Equal(before.Resources, after.Resources)},
		{"mounts", !slices.Equal(before.Mounts, after.Mounts)}, {"devices", !slices.Equal(before.Devices, after.Devices)},
	}
	actor := authority.Of(ctx)
	for _, change := range fields {
		if !change.changed {
			continue
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO machine_setting_changes(node_id,changed_ms,actor_role,actor_session,actor_room,field) VALUES(?,?,?,?,?,?)`, after.ID, after.Updated.UnixMilli(), string(actor.Role), actor.Session, actor.Room, change.name)
		if err != nil {
			return err
		}
	}
	return nil
}

// LatestFieldChanges returns the latest recorded edit to each setting.
func (s Store) LatestFieldChanges(ctx context.Context, id string) ([]FieldChange, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT field,changed_ms,actor_role,actor_session,actor_room FROM machine_setting_changes WHERE node_id=? AND rowid IN (SELECT max(rowid) FROM machine_setting_changes WHERE node_id=? GROUP BY field) ORDER BY field`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []FieldChange
	for rows.Next() {
		var c FieldChange
		var ms int64
		if err := rows.Scan(&c.Field, &ms, &c.Actor.Role, &c.Actor.Session, &c.Actor.Room); err != nil {
			return nil, err
		}
		c.Changed = time.UnixMilli(ms)
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

func updateMachine(ctx context.Context, conn *sql.Conn, m Machine) error {
	_, err := conn.ExecContext(ctx, `UPDATE machines SET name=?, ssh=?, enabled=?, local=?, updated_ms=?, health=`+healthOnToggle+`, host_checks_grants=?, rooms=?, labels=?, container_socket=?, ssh_connect_timeout_ms=?, check_tmp=?, max_io_heavy=?, max_disk_percent=?, resources=?, mounts=?, devices=?, quick_slots=? WHERE id=?`,
		m.Name, m.SSH, m.Enabled, m.Local, m.Updated.UnixMilli(), flag(m.Enabled), marshalGrants(m.HostChecksGrants), marshalNames(m.Rooms), marshalNames(m.Labels), m.ContainerSocket, m.SSHConnectTimeout.Milliseconds(), m.CheckTmp, m.MaxIOHeavy, m.MaxDiskPercent, marshalResources(m.Resources), marshalContainerRegistrations(m.Mounts), marshalContainerRegistrations(m.Devices), m.QuickSlots, m.ID)
	return err
}

// find reads the stored machine that m continues: the row with its ID, when it
// has one. A machine without an ID is new.
func find(ctx context.Context, q querier, m Machine) (Machine, bool, error) {
	if m.ID == "" {
		return Machine{}, false, nil
	}
	stored, err := getByID(ctx, q, m.ID)
	switch {
	case err == nil:
		return stored, true, nil
	case errors.Is(err, os.ErrNotExist):
		return Machine{}, false, nil
	default:
		return Machine{}, false, err
	}
}

// SetEnabled turns a machine's work on or off.
func (s Store) SetEnabled(ctx context.Context, name string, enabled bool) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + name}, func(conn *sql.Conn) error {
		if _, err := getOn(ctx, conn, name, false); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `UPDATE machines SET enabled=?, updated_ms=?, health=`+healthOnToggle+` WHERE name=?`, enabled, time.Now().UnixMilli(), flag(enabled), name)
		return err
	})
}

// Remove deletes a machine. It refuses the engine host, whose row is this
// data folder's own machine.
func (s Store) Remove(ctx context.Context, name string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + name}, func(conn *sql.Conn) error {
		m, err := getOn(ctx, conn, name, false)
		if err != nil {
			return err
		}
		if m.Local {
			return fmt.Errorf("node %q: %w", name, ErrLocal)
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM machines WHERE id=?`, m.ID)
		return err
	})
}

// marshalGrants stores a machine's host-check grants as the row's JSON.
func marshalGrants(grants []HostChecksGrant) string {
	raw, err := json.Marshal(uniqueGrants(grants))
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// marshalNames stores a name list in one stable order, so a row that names the
// same entries twice reads back the same way.
func marshalNames(names []string) string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	raw, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// marshalResources stores the node's declared resource counts as JSON.
func marshalResources(resources map[ResourceName]int) string {
	if len(resources) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(resources)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// marshalContainerRegistrations stores a node's room-scoped container access as JSON.
func marshalContainerRegistrations[T NamedMount | DeviceGrant](registrations []T) string {
	if len(registrations) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(registrations)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// newID names a new row. Names can change, so the ID is the stable handle.
func newID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("m_%x", time.Now().UnixNano())
	}
	return "m_" + hex.EncodeToString(raw[:])
}
