// Package machines keeps the one registry of machines that Dyna can use: the
// engine host, machines that run agents over SSH, and machines that run local
// CI. The engine host is one row, marked local, with the engine and agents
// roles, so the host and the CI nodes are no longer two lists.
package machines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Topic is the hub topic of a committed machine change. The engine wakes the
// CI workers on it, and clients see it.
const Topic = "machines"

// Role is one duty of a machine and the unit the engine schedules on.
type Role string

const (
	// RoleEngine runs this data folder's engine. Only the local row has it.
	RoleEngine Role = "engine"
	// RoleAgents runs agents for sessions and workflows.
	RoleAgents Role = "agents"
	// RoleCI runs local CI checks and merge trials.
	RoleCI Role = "ci"
)

// Roles is every role, in the order the CLI shows them.
var Roles = []Role{RoleEngine, RoleAgents, RoleCI}

// ParseRole reads a role name from user input.
func ParseRole(name string) (Role, error) {
	role := Role(strings.TrimSpace(name))
	if !slices.Contains(Roles, role) {
		return "", fmt.Errorf("role must be engine, agents or ci, not %q", name)
	}
	return role, nil
}

// Machine is one row of the registry.
type Machine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Roles   []Role `json:"roles"`
	SSH     string `json:"ssh"`
	CISlots int    `json:"ciSlots"`
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
	HealthReason HealthReason `json:"healthReason,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Load         Load         `json:"load"`
	// LastProbe is when the last probe started, and LastSeen when a probe last
	// reached the machine. Both are zero before the first probe.
	LastProbe  time.Time `json:"lastProbe"`
	LastSeen   time.Time `json:"lastSeen"`
	ProbeError string    `json:"probeError"`
}

// HasRole reports whether the machine has the role.
func (m Machine) HasRole(role Role) bool { return slices.Contains(m.Roles, role) }

// RoleNames joins the roles for a table cell.
func (m Machine) RoleNames() string {
	names := make([]string, 0, len(m.Roles))
	for _, role := range m.Roles {
		names = append(names, string(role))
	}
	return strings.Join(names, ",")
}

// ErrLocal marks a change that would remove or unmake the engine host.
var ErrLocal = errors.New("this machine runs the engine")

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var sshTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*(@[a-zA-Z0-9][a-zA-Z0-9_.-]*)?$`)
var invalidName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// ValidateName reports whether name can name a machine.
func ValidateName(name string) error {
	if !nodeName.MatchString(name) {
		return errors.New("machine name must contain only letters, numbers, dots, underscores or hyphens, and start with a letter or number")
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
	if len(m.Roles) == 0 {
		return errors.New("a machine needs at least one role: engine, agents or ci")
	}
	for _, role := range m.Roles {
		if !slices.Contains(Roles, role) {
			return fmt.Errorf("role %q must be engine, agents or ci", role)
		}
	}
	if m.HasRole(RoleEngine) && !m.Local {
		return fmt.Errorf("only this machine has the engine role: %w", ErrLocal)
	}
	if m.Local && !m.HasRole(RoleEngine) {
		return fmt.Errorf("this machine always has the engine role: %w", ErrLocal)
	}
	if m.Local && m.SSH != "" {
		return errors.New("this machine needs no SSH destination")
	}
	if !m.Local && m.SSH == "" {
		return errors.New("a machine other than this one needs an SSH destination")
	}
	if m.SSH != "" {
		if err := ValidateSSH(m.SSH); err != nil {
			return err
		}
	}
	if m.HasRole(RoleCI) && m.CISlots < 1 {
		return errors.New("a machine with the ci role needs at least one CI slot")
	}
	return nil
}

const schemaVersion = 3

const schema = `CREATE TABLE machines (
	id             TEXT    PRIMARY KEY,
	name           TEXT    NOT NULL UNIQUE,
	roles          TEXT    NOT NULL CHECK(json_valid(roles)),
	ssh            TEXT    NOT NULL,
	ci_slots       INTEGER NOT NULL CHECK(ci_slots >= 0),
	enabled        INTEGER NOT NULL CHECK(enabled IN (0,1)),
	local          INTEGER NOT NULL CHECK(local IN (0,1)),
	created_ms     INTEGER NOT NULL,
	updated_ms     INTEGER NOT NULL,
	capabilities   TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(capabilities)),
	health         TEXT    NOT NULL DEFAULT 'unknown',
	health_reason  TEXT    NOT NULL DEFAULT '',
	last_probe_ms  INTEGER NOT NULL DEFAULT 0,
	last_seen_ms   INTEGER NOT NULL DEFAULT 0,
	probe_error    TEXT    NOT NULL DEFAULT '',
	last_load      TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(last_load))
);
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

// degradedColumns add the reason of a degraded health to a version 2 table.
// A row stored before the reason existed is online or offline, so it needs
// none.
var degradedColumns = []string{
	`ALTER TABLE machines ADD COLUMN health_reason TEXT NOT NULL DEFAULT ''`,
}

// registryColumns are the columns a machine edit writes. The probe columns
// change only through SaveProbe.
const registryColumns = `id,name,roles,ssh,ci_slots,enabled,local,created_ms,updated_ms`

const columns = registryColumns + `,capabilities,health,health_reason,last_probe_ms,last_seen_ms,probe_error,last_load`

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
// imported the old local CI nodes; version 2 added the probe columns, and
// version 3 added the reason of a degraded health.
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
	if old == 1 {
		if err := addProbeColumns(ctx, conn); err != nil {
			return err
		}
	}
	return addDegradedColumn(ctx, conn)
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
	_, err = conn.ExecContext(ctx, `INSERT INTO machines(`+registryColumns+`) VALUES(?,?,?,'',0,1,1,?,?)`,
		newID(), name, marshalRoles([]Role{RoleEngine, RoleAgents}), now, now)
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

// importLocalCINodes turns the old local CI node rows into machines with the
// ci role and drops their table. A node with no SSH destination ran on this
// machine, so it merges into the local row.
//
// TODO: legacy/shim remove later — imports local_ci_nodes into machines
func importLocalCINodes(ctx context.Context, conn *sql.Conn) error {
	present, err := database.HasTable(ctx, conn, "local_ci_nodes")
	if err != nil || !present {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT name,ssh,slots,enabled FROM local_ci_nodes ORDER BY name`)
	if err != nil {
		return err
	}
	type oldNode struct {
		name, ssh string
		slots     int
		enabled   bool
	}
	var old []oldNode
	for rows.Next() {
		var n oldNode
		if err := rows.Scan(&n.name, &n.ssh, &n.slots, &n.enabled); err != nil {
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
		if err := importNode(ctx, conn, n.name, n.ssh, n.slots, n.enabled); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `DROP TABLE local_ci_nodes`)
	return err
}

// importNode stores one old CI node row as a machine.
func importNode(ctx context.Context, conn *sql.Conn, name, ssh string, slots int, enabled bool) error {
	now := time.Now().UnixMilli()
	if ssh == "" {
		// A disabled local node stays off after the upgrade, so its CI work does
		// not start on this machine unasked. Its slots come with the ci role.
		if !enabled {
			return nil
		}
		m, err := getOn(ctx, conn, "", true)
		if err != nil {
			return err
		}
		m.Roles = append(m.Roles, RoleCI)
		// The engine host stays enabled: the old node's switch covered its CI
		// work only, and this row also runs the engine and its agents.
		if _, err := conn.ExecContext(ctx, `UPDATE machines SET roles=?, ssh='', ci_slots=?, updated_ms=? WHERE id=?`,
			marshalRoles(m.Roles), max(slots, 1), now, m.ID); err != nil {
			return err
		}
		return nil
	}
	name, err := freeName(ctx, conn, sanitizeName(name))
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO machines(`+registryColumns+`) VALUES(?,?,?,?,?,?,0,?,?)`,
		newID(), name, marshalRoles([]Role{RoleCI}), ssh, slots, enabled, now, now)
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
			return Machine{}, fmt.Errorf("this machine is not in the registry: %w", os.ErrNotExist)
		}
		return Machine{}, fmt.Errorf("machine %q: %w", name, os.ErrNotExist)
	}
	return m, err
}

// getByID reads the machine with this ID.
func getByID(ctx context.Context, q querier, id string) (Machine, error) {
	m, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, fmt.Errorf("machine %s: %w", id, os.ErrNotExist)
	}
	return m, err
}

// scanner reads one machine row.
type scanner interface{ Scan(...any) error }

func scan(row scanner) (Machine, error) {
	var m Machine
	var roles, capabilities, health, reason, load, probeError string
	var created, updated, probed, seen int64
	if err := row.Scan(&m.ID, &m.Name, &roles, &m.SSH, &m.CISlots, &m.Enabled, &m.Local, &created, &updated,
		&capabilities, &health, &reason, &probed, &seen, &probeError, &load); err != nil {
		return Machine{}, err
	}
	if err := json.Unmarshal([]byte(roles), &m.Roles); err != nil {
		return Machine{}, fmt.Errorf("machine %s roles: %w", m.Name, err)
	}
	if err := json.Unmarshal([]byte(capabilities), &m.Capabilities); err != nil {
		return Machine{}, fmt.Errorf("machine %s capabilities: %w", m.Name, err)
	}
	if err := json.Unmarshal([]byte(load), &m.Load); err != nil {
		return Machine{}, fmt.Errorf("machine %s load: %w", m.Name, err)
	}
	m.Health, m.HealthReason, m.ProbeError = parseHealth(health), parseHealthReason(reason), probeError
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

// reasonOnToggle drops the reason of a degraded health once the machine is off
// or comes back, so a stored row never claims a reason it no longer holds. It
// reads the row's old health, as healthOnToggle does.
const reasonOnToggle = `CASE WHEN ? = 0 OR health IN ('disabled','unknown') THEN '' ELSE health_reason END`

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
	if !stored.Enabled {
		health = HealthDisabled
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	// A machine that is off or unreachable has no reason of its own: only a
	// probe that reached it can degrade it.
	reason := ReasonNone
	if health == HealthOnline || health == HealthDegraded {
		reason = r.Reason
	} else {
		health = r.Health
	}
	// Only a probe that reached the machine replaces what the last good probe
	// read, so a machine that is off keeps its capabilities for the owner.
	var capabilities, load string
	if health == HealthOnline || health == HealthDegraded {
		rawCaps, err := json.Marshal(r.Capabilities)
		if err != nil {
			return Machine{}, err
		}
		rawLoad, err := json.Marshal(r.Load)
		if err != nil {
			return Machine{}, err
		}
		capabilities, load = string(rawCaps), string(rawLoad)
	}
	probeErr := ""
	if health == HealthOffline {
		probeErr = r.Err
	}
	probed := r.At.UnixMilli()
	// A probe that starts or ends a hold changes where CI work can run, so it
	// wakes the CI workers. Any other probe result changes no work.
	key := probeKeyPrefix + stored.Name
	if heldAfter := holdsCI(health, reason); heldAfter != holdsCI(stored.Health, stored.HealthReason) {
		key = holdKeyPrefix + stored.Name
	}
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: key}, func(conn *sql.Conn) error {
		if health == HealthOnline || health == HealthDegraded {
			_, err := conn.ExecContext(ctx, `UPDATE machines SET capabilities=?, last_load=?, health=?, health_reason=?, last_probe_ms=?, last_seen_ms=?, probe_error='' WHERE id=?`,
				capabilities, load, string(health), string(reason), probed, probed, id)
			return err
		}
		if health == HealthDisabled {
			_, err := conn.ExecContext(ctx, `UPDATE machines SET health=?, health_reason='', last_probe_ms=?, probe_error='' WHERE id=?`,
				string(health), probed, id)
			return err
		}
		_, err := conn.ExecContext(ctx, `UPDATE machines SET health=?, health_reason='', last_probe_ms=?, probe_error=? WHERE id=?`,
			string(health), probed, probeErr, id)
		return err
	})
	if err != nil {
		return Machine{}, err
	}
	return getByID(ctx, db, id)
}

// holdsCI reports whether a stored health and reason hold CI work.
func holdsCI(health Health, reason HealthReason) bool {
	return health == HealthDegraded && reason == ReasonLowDisk
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
	m.Roles = uniqueRoles(m.Roles)
	if !m.HasRole(RoleCI) {
		m.CISlots = 0
	}
	m.Updated = now
	if err := m.Validate(); err != nil {
		return Machine{}, err
	}
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + m.Name}, func(conn *sql.Conn) error {
		if found {
			_, err := conn.ExecContext(ctx, `UPDATE machines SET name=?, roles=?, ssh=?, ci_slots=?, enabled=?, local=?, updated_ms=?, health=`+healthOnToggle+`, health_reason=`+reasonOnToggle+` WHERE id=?`,
				m.Name, marshalRoles(m.Roles), m.SSH, m.CISlots, m.Enabled, m.Local, m.Updated.UnixMilli(), flag(m.Enabled), flag(m.Enabled), m.ID)
			return err
		}
		extant, err := getOn(ctx, conn, m.Name, false)
		if err == nil && extant.ID != m.ID {
			return fmt.Errorf("machine %q exists: %w", m.Name, os.ErrExist)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO machines(`+registryColumns+`,health) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			m.ID, m.Name, marshalRoles(m.Roles), m.SSH, m.CISlots, m.Enabled, m.Local,
			m.Created.UnixMilli(), m.Updated.UnixMilli(), string(healthOnCreate(m.Enabled)))
		return err
	})
	if err != nil {
		return Machine{}, err
	}
	return m, nil
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

// RemoveRole drops one role from a machine and removes the machine when that
// was its last role. The engine host keeps its row because it always has the
// engine role.
func (s Store) RemoveRole(ctx context.Context, name string, role Role) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "machine:" + name}, func(conn *sql.Conn) error {
		m, err := getOn(ctx, conn, name, false)
		if err != nil {
			return err
		}
		if !m.HasRole(role) {
			return fmt.Errorf("machine %q has no %s role: %w", name, role, os.ErrNotExist)
		}
		m.Roles = slices.DeleteFunc(m.Roles, func(r Role) bool { return r == role })
		if len(m.Roles) == 0 {
			if m.Local {
				return fmt.Errorf("machine %q: %w", name, ErrLocal)
			}
			_, err := conn.ExecContext(ctx, `DELETE FROM machines WHERE id=?`, m.ID)
			return err
		}
		if !m.HasRole(RoleCI) {
			m.CISlots = 0
		}
		if err := m.Validate(); err != nil {
			return err
		}
		m.Updated = time.Now()
		_, err = conn.ExecContext(ctx, `UPDATE machines SET roles=?, ci_slots=?, updated_ms=? WHERE id=?`,
			marshalRoles(m.Roles), m.CISlots, m.Updated.UnixMilli(), m.ID)
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
			return fmt.Errorf("machine %q: %w", name, ErrLocal)
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM machines WHERE id=?`, m.ID)
		return err
	})
}

// uniqueRoles drops repeated roles, keeping the order the caller gave.
func uniqueRoles(roles []Role) []Role {
	out := make([]Role, 0, len(roles))
	for _, role := range roles {
		if !slices.Contains(out, role) {
			out = append(out, role)
		}
	}
	return out
}

func marshalRoles(roles []Role) string {
	raw, err := json.Marshal(uniqueRoles(roles))
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
