package worktrees

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const (
	portMin   = 20000
	portSlots = 1000 // blocks in the 20000-29999 range
	blockSize = 10
)

// blockFor returns the first port of the block that path prefers.
func blockFor(path string) int {
	h := fnv.New32a()
	h.Write([]byte(path))
	return portMin + blockSize*int(h.Sum32()%portSlots)
}

// allocatePort returns the first port of a free block for path. A block is
// free when no record holds its first port and isFree accepts all its ports.
// It tries the preferred block first, then the following blocks, wrapping at
// the end of the range.
func allocatePort(path string, records []Record, isFree func(int) bool) (int, error) {
	taken := map[int]bool{}
	for _, rec := range records {
		taken[rec.Port] = true
	}
	first := blockFor(path)
	for i := 0; i < portSlots; i++ {
		start := portMin + (first-portMin+i*blockSize)%(portSlots*blockSize)
		if taken[start] || !blockFree(start, isFree) {
			continue
		}
		return start, nil
	}
	return 0, fmt.Errorf("no free port block of %d ports in %d-%d; run dyna git worktrees gc", blockSize, portMin, portMin+portSlots*blockSize-1)
}

func blockFree(start int, isFree func(int) bool) bool {
	for port := start; port < start+blockSize; port++ {
		if !isFree(port) {
			return false
		}
	}
	return true
}

// reclaimPortBlock frees one port block when the pool is full. A record whose
// directory is proven absent is deleted. Otherwise the oldest record whose
// recorded and inferred owners are all closed, and whose block is free of
// sockets, only loses its port; the tree stays. Dirty and unmerged trees are
// eligible because no file or branch changes. Ownership scans run once per
// repository, so the cost does not grow with the number of trees. Every change
// compares the record read earlier, so a record that another caller replaced
// meanwhile is left alone. It reports whether a block was freed.
func reclaimPortBlock(ctx context.Context, db *sql.DB, records []Record, except string) bool {
	var held []Record
	freed := false
	for _, rec := range records {
		if rec.Port == 0 || rec.Path == except {
			continue
		}
		if provenMissingPath(rec.Path) {
			// Only a proven absent path counts; any other error is uncertain.
			if ok, err := releaseRecord(ctx, db, rec, true); err == nil && ok {
				freed = true
			}
			continue
		}
		held = append(held, rec)
	}
	if freed {
		return true
	}
	slices.SortFunc(held, func(a, b Record) int {
		return cmp.Or(a.RetainedAt.Compare(b.RetainedAt), a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.Path, b.Path))
	})
	reclaimable := map[string]map[string]bool{}
	for _, rec := range held {
		if !heldPortReclaimable(ctx, rec, reclaimable) {
			continue
		}
		if ok, err := releaseRecord(ctx, db, rec, false); err == nil && ok {
			return true
		}
	}
	return false
}

// reclaimablePortBlockCount counts records whose blocks allocation can reuse.
// It is read-only, so capacity alerts can use the same eligibility checks as
// allocation without changing records. The repository cache bounds ownership
// scans to one per repository, however many candidate blocks it holds.
// Tests replace it to isolate alert state transitions from the host.
var reclaimablePortBlockCount = func(ctx context.Context, records []Record) int {
	count := 0
	reclaimable := map[string]map[string]bool{}
	for _, rec := range records {
		if rec.Port == 0 {
			continue
		}
		if provenMissingPath(rec.Path) {
			// Reclaiming the record does not free a socket held by another process.
			if blockFree(rec.Port, portFree) {
				count++
			}
			continue
		}
		if heldPortReclaimable(ctx, rec, reclaimable) {
			count++
		}
	}
	return count
}

// provenMissingPath is true only when the path is positively absent.
func provenMissingPath(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// heldPortReclaimable applies the closed-owner and free-socket checks to a
// record whose path was not proven absent. Unknown owners and errors keep it.
func heldPortReclaimable(ctx context.Context, rec Record, reclaimable map[string]map[string]bool) bool {
	if rec.Repo == "" || !blockFree(rec.Port, portFree) {
		return false
	}
	paths, seen := reclaimable[rec.Repo]
	if !seen {
		paths = reclaimablePaths(ctx, rec.Repo)
		reclaimable[rec.Repo] = paths
	}
	return paths[filepath.Clean(rec.Path)]
}

// releaseRecord deletes orig, or clears its port, in one transaction. It does
// nothing and reports false when the stored record no longer equals orig.
func releaseRecord(ctx context.Context, db *sql.DB, orig Record, remove bool) (bool, error) {
	want, err := json.Marshal(orig)
	if err != nil {
		return false, err
	}
	released := false
	// Worktree records have no change-bus consumer.
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		current, err := loadRecords(ctx, conn, "WHERE path=?", orig.Path)
		if err != nil || len(current) != 1 {
			return err
		}
		have, err := json.Marshal(current[0])
		if err != nil || !bytes.Equal(have, want) {
			return err
		}
		if remove {
			if _, err = conn.ExecContext(ctx, `DELETE FROM worktree_records WHERE path=?`, orig.Path); err != nil {
				return err
			}
		} else {
			cleared := orig
			cleared.Port = 0
			if err = saveRecord(ctx, conn, cleared); err != nil {
				return err
			}
		}
		// A release that drops use below the 90% band rearms the next alert period.
		if err = rearmPortCapacity(ctx, conn); err != nil {
			return err
		}
		released = true
		return nil
	})
	return released, err
}

// reclaimablePaths returns trees whose recorded and inferred owners are all
// closed. It ignores file and merge status because reclaiming a port preserves
// the tree. Locked, unknown, unreadable and open-owned trees keep their ports.
// One scan covers every tree of repo without fetching or removing anything.
// An uncertain scan returns none. Tests replace it.
var reclaimablePaths = func(ctx context.Context, repo string) map[string]bool {
	trees, err := scan(ctx, repo, &GCOptions{history: &runHistory{}})
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, t := range trees {
		if portReclaimable(t) {
			out[filepath.Clean(t.Path)] = true
		}
	}
	return out
}

// portReclaimable requires positive proof that every owner is closed. Unlike
// tree removal, port reclamation does not care about dirty files or ancestry.
func portReclaimable(t Tree) bool {
	if t.protected != "" || t.sessionID == "unverified" {
		return false
	}
	known := false
	if t.runID != "" {
		closed, err := workflowClosed(t.runID)
		if err != nil || !closed {
			return false
		}
		known = true
	}
	if t.sessionID != "" {
		closed, err := SessionClosed(t.sessionID)
		if err != nil || !closed {
			return false
		}
		known = true
	}
	for _, owner := range t.inferred.Sessions {
		if !owner.Closed {
			return false
		}
		closed, err := SessionClosed(owner.ID)
		if err != nil || !closed {
			return false
		}
		known = true
	}
	for _, owner := range t.inferred.Runs {
		if !owner.Closed {
			return false
		}
		known = true
	}
	return known
}

// portFree decides whether a port is free. Tests replace it to force a busy host.
var portFree = canBind

// canBind reports whether port on 127.0.0.1 is free right now.
func canBind(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	return l.Close() == nil
}

// PortRange returns the block as "first-last", or "" when the worktree has no port.
func PortRange(rec Record) string {
	if rec.Port == 0 {
		return ""
	}
	return fmt.Sprintf("%d-%d", rec.Port, rec.Port+blockSize-1)
}

// PortEnv returns the port variables for a worktree, or nil when it has no port.
func PortEnv(rec Record) map[string]string {
	if rec.Port == 0 {
		return nil
	}
	return map[string]string{
		"DYNA_PORT":  strconv.Itoa(rec.Port),
		"DYNA_PORTS": PortRange(rec),
		"PORT":       strconv.Itoa(rec.Port),
	}
}

// PortEnvForDir returns the port variables a worker in dir should get.
// A directory outside every tracked worktree gets the same names set to empty,
// so a worker never keeps the ports of the worktree that started it.
func PortEnvForDir(ctx context.Context, dir string) map[string]string {
	rec, err := ForDir(ctx, dir)
	if err == nil && rec.Port != 0 {
		return PortEnv(rec)
	}
	return map[string]string{"DYNA_PORT": "", "DYNA_PORTS": "", "PORT": ""}
}

// ensurePort allocates a released block without replacing any other record
// field. The transaction reloads the current row, so concurrent retention or
// ownership updates survive, and a removed record is never recreated.
func ensurePort(ctx context.Context, path string) (Record, error) {
	safe, err := recordKey(path)
	if err != nil {
		return Record{}, err
	}
	db, err := registryDB()
	if err != nil {
		return Record{}, err
	}
	for {
		records, err := loadRecords(ctx, db, "")
		if err != nil {
			return Record{}, err
		}
		current := -1
		for i := range records {
			if records[i].Path == safe {
				current = i
				break
			}
		}
		if current < 0 {
			return Record{}, fmt.Errorf("worktree %s is not registered", safe)
		}
		if records[current].Port != 0 {
			return records[current], nil
		}
		port, err := allocatePort(safe, records, portFree)
		if err != nil {
			if reclaimPortBlock(ctx, db, records, safe) {
				continue
			}
			return Record{}, fmt.Errorf("allocate worktree port: %w", err)
		}
		var allocated Record
		// Worktree records have no change-bus consumer.
		err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			current, err := loadRecords(ctx, conn, "WHERE path=?", safe)
			if err != nil {
				return err
			}
			if len(current) != 1 {
				return fmt.Errorf("worktree %s is not registered", safe)
			}
			if current[0].Port != 0 {
				allocated = current[0]
				return nil
			}
			records, err := loadRecords(ctx, conn, "")
			if err != nil {
				return err
			}
			for _, other := range records {
				if other.Path != safe && other.Port == port {
					return errPortClaimed
				}
			}
			current[0].Port = port
			if err := saveRecord(ctx, conn, current[0]); err != nil {
				return err
			}
			allocated = current[0]
			return nil
		})
		if !errors.Is(err, errPortClaimed) {
			return allocated, err
		}
	}
}

// ForDir returns the record of the registered worktree that contains dir.
// Git reports physical paths, while records keep the configured path, so the
// match resolves symlinks on both sides.
func ForDir(ctx context.Context, dir string) (Record, error) {
	root, err := RepositoryRoot(ctx, dir)
	if err != nil {
		return Record{}, err
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Record{}, fmt.Errorf("resolve worktree root: %w", err)
	}
	records, err := List()
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if got, err := filepath.EvalSymlinks(rec.Path); err == nil && got == want {
			if rec.Port == 0 {
				return ensurePort(ctx, rec.Path)
			}
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("worktree %s is not registered", root)
}
