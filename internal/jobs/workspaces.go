package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/state"
)

// workspaceSchema keeps one active workspace per session, node and repository.
// A cleaning row is a deleted workspace whose node folder still needs removal;
// its unique path lets a new job start a fresh active workspace without waiting.
const workspaceSchema = `
CREATE TABLE IF NOT EXISTS workspaces (
	node TEXT NOT NULL,
	session TEXT NOT NULL,
	repo TEXT NOT NULL,
	path TEXT NOT NULL,
	created_ms INTEGER NOT NULL,
	last_used_ms INTEGER NOT NULL,
	closed INTEGER NOT NULL DEFAULT 0,
	cleaning INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(node, path)
);
CREATE UNIQUE INDEX IF NOT EXISTS workspaces_active
ON workspaces(node, session, repo) WHERE cleaning=0;
CREATE INDEX IF NOT EXISTS workspaces_last_used ON workspaces(last_used_ms);`

// WorkspaceTopic is the hub topic of committed workspace lifecycle changes.
const WorkspaceTopic = "workspaces"

const workspaceIdleLimit = 24 * time.Hour

// workspace is one kept checkout and its cleanup state.
type workspace struct {
	Node     string
	Session  string
	Repo     string
	Path     string
	Created  time.Time
	LastUsed time.Time
	Closed   bool
	Cleaning bool
}

// saveSubmitted stores a new job and its workspace membership atomically. The
// returned job names the existing active workspace when one was already kept.
func (s Store) saveSubmitted(ctx context.Context, job Job) (Job, error) {
	return s.save(ctx, job, true)
}

// workspaceResource is the internal one-unit admission lock of one session's
// repository workspace. Node queues are separate, so the node needs no hash.
func workspaceResource(session, repo string) machines.ResourceName {
	return machines.ResourceName("dyna.workspace." + RepoKey(session+"\x00"+repo))
}

func isWorkspaceResource(name machines.ResourceName) bool {
	return strings.HasPrefix(string(name), "dyna.workspace.")
}

// newWorkspacePath makes a generation-unique path. Cleanup can mark an old
// generation for removal while a later submission creates a fresh one safely.
func newWorkspacePath(node, session, repo, jobID string) string {
	return path.Join(NodeBase(node), "workspaces", RepoKey(session+"\x00"+repo), jobID)
}

// ensureWorkspace reuses the active generation or inserts the job's candidate.
// The caller holds the SQLite writer transaction that also inserts the job.
func ensureWorkspace(ctx context.Context, conn *sql.Conn, job Job, now time.Time) (string, error) {
	var kept string
	err := conn.QueryRowContext(ctx, "SELECT path FROM workspaces WHERE node=? AND session=? AND repo=? AND cleaning=0", job.Node, job.Session, job.Repo).Scan(&kept)
	if err == nil {
		_, err = conn.ExecContext(ctx, "UPDATE workspaces SET last_used_ms=?, closed=0 WHERE node=? AND path=? AND cleaning=0", now.UnixMilli(), job.Node, kept)
		return kept, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	ms := now.UnixMilli()
	_, err = conn.ExecContext(ctx, "INSERT INTO workspaces(node,session,repo,path,created_ms,last_used_ms) VALUES(?,?,?,?,?,?)", job.Node, job.Session, job.Repo, job.Workspace, ms, ms)
	return job.Workspace, err
}

// touchWorkspace moves the idle deadline after a job ends.
func touchWorkspace(ctx context.Context, conn *sql.Conn, kept string, now time.Time) error {
	_, err := conn.ExecContext(ctx, "UPDATE workspaces SET last_used_ms=? WHERE path=? AND cleaning=0", now.UnixMilli(), kept)
	return err
}

// markSessionClosed makes cleanup durable when a live job defers removal.
func (s Store) markSessionClosed(ctx context.Context, session string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: WorkspaceTopic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE workspaces SET closed=1 WHERE session=?", session)
		return err
	})
}

func scanWorkspace(row interface{ Scan(...any) error }) (workspace, error) {
	var w workspace
	var created, used int64
	var closed, cleaning int
	if err := row.Scan(&w.Node, &w.Session, &w.Repo, &w.Path, &created, &used, &closed, &cleaning); err != nil {
		return w, err
	}
	w.Created = time.UnixMilli(created)
	w.LastUsed = time.UnixMilli(used)
	w.Closed = closed == 1
	w.Cleaning = cleaning == 1
	return w, nil
}

// listWorkspaces returns the least recently used first for disk cleanup.
func (s Store) listWorkspaces(ctx context.Context) ([]workspace, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT node,session,repo,path,created_ms,last_used_ms,closed,cleaning FROM workspaces ORDER BY last_used_ms,node,path")
	if err != nil {
		return nil, err
	}
	var out []workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// claimWorkspace excludes a live job and changes the generation to cleaning in
// one write. A later submission cannot reuse this path. The last-used check
// rejects a stale cleanup decision after another submission touched the row.
func (s Store) claimWorkspace(ctx context.Context, w workspace) (bool, error) {
	db, err := s.db()
	if err != nil {
		return false, err
	}
	claimed := false
	err = state.For(db).Write(ctx, hub.Event{Topic: WorkspaceTopic}, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, "UPDATE workspaces SET cleaning=1\nWHERE node=? AND path=? AND last_used_ms=?\nAND NOT EXISTS (SELECT 1 FROM jobs WHERE workspace=? AND state IN ('queued','running'))", w.Node, w.Path, w.LastUsed.UnixMilli(), w.Path)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		claimed = n == 1
		return err
	})
	return claimed, err
}

// removeWorkspace forgets a cleaning row after its node folder is gone. If the
// row write fails, the next pass repeats a harmless rm and retries the row.
func (s Store) removeWorkspace(ctx context.Context, w workspace) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: WorkspaceTopic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM workspaces WHERE node=? AND path=? AND cleaning=1", w.Node, w.Path)
		return err
	})
}

// WorkspaceCleanup selects one maintenance pass. Session marks that closed
// session's workspaces for removal; empty checks all sessions. Now is injected
// by tests and by the maintenance pass's clock.
type WorkspaceCleanup struct {
	Session string
	Now     time.Time
}

// nodeDiskFree measures the filesystem that holds one node's workspaces.
func nodeDiskFree(ctx context.Context, x Executor, node string) (uint64, error) {
	var out TailBuffer
	if err := x.NodeCommand(ctx, &out, &out, "df -Pk "+shellquote.Quote(NodeBase(node))); err != nil {
		return 0, fmt.Errorf("df: %w: %s", err, out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("df returned no filesystem row: %q", out.String())
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0, fmt.Errorf("df returned a short filesystem row: %q", lines[1])
	}
	blocks, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("df available blocks %q: %w", fields[3], err)
	}
	if blocks > ^uint64(0)/1024 {
		return 0, fmt.Errorf("df available blocks %q overflows bytes", fields[3])
	}
	return blocks * 1024, nil
}

// CleanupWorkspaces removes expired folders on their nodes and then their rows.
// Closed sessions and idle workspaces expire always. When a node is below its
// admission disk floor, its workspaces expire least recently used first. A
// fresh disk measurement stops pressure cleanup as soon as the floor is met.
func (s *Service) CleanupWorkspaces(ctx context.Context, options WorkspaceCleanup) (int, error) {
	if options.Session != "" {
		if err := s.Store.markSessionClosed(ctx, options.Session); err != nil {
			return 0, err
		}
	}
	rows, err := s.Store.listWorkspaces(ctx)
	if err != nil {
		return 0, err
	}
	now := options.Now
	if now.IsZero() {
		now = s.Store.now()
	}
	type machineAnswer struct {
		machine machines.Machine
		err     error
	}
	answers := map[string]machineAnswer{}
	machineFor := func(node string) machineAnswer {
		if answer, ok := answers[node]; ok {
			return answer
		}
		answer := machineAnswer{}
		answer.machine, answer.err = s.Machines.Get(ctx, node)
		answers[node] = answer
		return answer
	}
	pressureDone := map[string]bool{}
	removed := 0
	var errs []error
	for _, w := range rows {
		if options.Session != "" && w.Session != options.Session {
			continue
		}
		expired := w.Cleaning || w.Closed || now.Sub(w.LastUsed) >= workspaceIdleLimit
		answer := machineAnswer{}
		if !expired {
			answer = machineFor(w.Node)
			if answer.err != nil {
				errs = append(errs, fmt.Errorf("workspace %s: resolve node %s: %w", w.Path, w.Node, answer.err))
				continue
			}
			expired = answer.machine.DiskBlock() != "" && !pressureDone[w.Node]
		}
		if !expired {
			continue
		}
		claimed, err := s.Store.claimWorkspace(ctx, w)
		if err != nil {
			errs = append(errs, fmt.Errorf("claim workspace %s: %w", w.Path, err))
			continue
		}
		if !claimed {
			continue
		}
		if answer.machine.Name == "" && answer.err == nil {
			answer = machineFor(w.Node)
		}
		if answer.err != nil {
			errs = append(errs, fmt.Errorf("workspace %s: resolve node %s: %w", w.Path, w.Node, answer.err))
			continue
		}
		conn := Conn{Name: answer.machine.Name, SSH: answer.machine.SSH, SSHConnectTimeout: answer.machine.SSHConnectTimeout}
		x := s.executor(conn)
		var out TailBuffer
		if err := x.NodeCommand(ctx, &out, &out, "rm -rf -- "+shellquote.Quote(w.Path)); err != nil {
			errs = append(errs, fmt.Errorf("remove workspace %s on %s: %w: %s", w.Path, w.Node, err, out.String()))
			continue
		}
		if err := s.Store.removeWorkspace(ctx, w); err != nil {
			errs = append(errs, fmt.Errorf("forget workspace %s: %w", w.Path, err))
			continue
		}
		removed++
		if answer.machine.DiskBlock() == "" || pressureDone[w.Node] {
			continue
		}
		free, err := nodeDiskFree(ctx, x, w.Node)
		if err != nil {
			pressureDone[w.Node] = true
			errs = append(errs, fmt.Errorf("recheck disk on %s: %w", w.Node, err))
			continue
		}
		answer.machine.Capabilities.Disk.Free = free
		answers[w.Node] = answer
		pressureDone[w.Node] = answer.machine.DiskBlock() == ""
	}
	return removed, errors.Join(errs...)
}
