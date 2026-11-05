package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// EngineRelocation is an owner-validated change of engine machine, not of physical host ownership.
// Construct it with NewEngineRelocation at the offline CLI boundary.
type EngineRelocation struct {
	from, to contract.HostName
	fromSSH  string
}

var (
	relocationHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	relocationSSH  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// NewEngineRelocation validates owner input once. SSH uses the host-add user@host grammar;
// custom ports belong in SSH config. It does not resolve names or contact either machine.
func NewEngineRelocation(from, fromSSH, to string) (EngineRelocation, error) {
	if !relocationHost.MatchString(from) || !relocationHost.MatchString(to) || from == to {
		return EngineRelocation{}, fmt.Errorf("--from and --to must name distinct hosts (letters, digits, hyphens and underscores)")
	}
	if !relocationSSH.MatchString(fromSSH) {
		return EngineRelocation{}, fmt.Errorf("--from-ssh must be the former engine's remote user@host endpoint")
	}
	_, host, _ := strings.Cut(fromSSH, "@")
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip, err := netip.ParseAddr(host)
	// Refuse legacy shortened/octal/integer/hex IPv4 spellings that resolvers may treat as loopback.
	if err != nil && (strings.Trim(host, "0123456789.") == "" || strings.HasPrefix(host, "0x")) {
		return EngineRelocation{}, fmt.Errorf("--from-ssh must use a remote DNS name or a full dotted-decimal IPv4 address")
	}
	if host == "localhost" || host == "localhost.localdomain" || strings.HasSuffix(host, ".localhost") || err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return EngineRelocation{}, fmt.Errorf("--from-ssh must not be loopback: the former engine becomes remote")
	}
	return EngineRelocation{from: contract.HostName(from), to: contract.HostName(to), fromSSH: fromSSH}, nil
}

// RelocateEngine takes the engine's exclusive lock before opening an existing store. It never
// calls Open: initialization and migrations must not run before relocation has been validated.
// This fences one data folder, not independent copies on two machines; the owner stops both.
func RelocateEngine(ctx context.Context, dir string, move EngineRelocation) error {
	layout := box.Layout{Data: dir}
	lock, err := lockStore(layout)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(layout.Store())+"?mode=rw&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var format, version string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='format'").Scan(&format); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='schema_version'").Scan(&version); err != nil {
		return err
	}
	if format != "dyna-v2" || version != strconv.Itoa(schemaVersion) {
		return fmt.Errorf("offline relocation requires dyna-v2 schema %d; use the matching binary, without migrating the copy", schemaVersion)
	}
	for _, host := range []contract.HostName{move.from, move.to} {
		var name contract.HostName
		if err := tx.QueryRowContext(ctx, "SELECT name FROM hosts WHERE name=?", host).Scan(&name); err != nil {
			return fmt.Errorf("host %s: %w", host, err)
		}
	}
	activeBoxes, err := listIDs[contract.BoxID](ctx, tx, "SELECT id FROM boxes WHERE host=? AND state NOT IN (?,?,?,?) ORDER BY id", move.from, contract.BoxStopped, contract.BoxDeleted, contract.BoxPending, contract.BoxFailed)
	if err != nil {
		return err
	}
	if len(activeBoxes) != 0 {
		return fmt.Errorf("former engine %s has active boxes %v; pause their rooms, refresh and wait for stopped box rows before copying", move.from, activeBoxes)
	}
	activeRuns, err := listIDs[string](ctx, tx, "SELECT id FROM subscription_runs WHERE host=? ORDER BY id", move.from)
	if err != nil {
		return err
	}
	if len(activeRuns) != 0 {
		return fmt.Errorf("finish subscription Runs %v on former engine %s before copying", activeRuns, move.from)
	}
	var localRuns int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM hostexec_runs WHERE host=? AND ended_at IS NULL", contract.ControlPlane).Scan(&localRuns); err != nil {
		return err
	}
	if localRuns != 0 {
		return fmt.Errorf("finish or explicitly stop unfinished control-plane host-exec runs before relocating")
	}
	rows, err := tx.QueryContext(ctx, "SELECT args FROM operations WHERE kind=? AND state IN (?,?,?)", (contract.HostexecRunArgs{}).OpKind(), contract.OpPending, contract.OpApproved, contract.OpRunning)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var args contract.HostexecRunArgs
		if err := json.Unmarshal(data, &args); err != nil {
			return err
		}
		if args.Host == contract.ControlPlane {
			return fmt.Errorf("finish or withdraw pending control-plane host-exec operations before relocating")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosts SET ssh=?,engine_host=0 WHERE name=?", move.fromSSH, move.from); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosts SET engine_host=1 WHERE name=?", move.to); err != nil {
		return err
	}
	return tx.Commit()
}
