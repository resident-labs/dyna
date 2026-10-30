package terminalsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/listpage"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shells"
	"github.com/charmbracelet/x/ansi"
)

// TerminalDirectory selects a room repo or the session's actual working
// directory. Session worktrees must not resolve back to the main git root.
func (s *Service) TerminalDirectory(ref string) (roomID, sessionID, cwd string, err error) {
	if runstore.IsSessionDirID(ref) {
		resolvedRoom, resolvedSession, resolvedCwd, e := s.options.SessionDirectory(ref)
		if e != nil {
			err = e
			return
		}
		if resolvedRoom == "" {
			err = errors.New("session has no room")
			return
		}
		roomID, sessionID, cwd = resolvedRoom, resolvedSession, resolvedCwd
	} else {
		rec, e := s.options.Rooms.Resolve(ref)
		if e != nil {
			err = e
			return
		}
		roomID = rec.ID
		cwd = rec.Repo
	}
	if cwd == "" {
		err = errors.New("this room has no repository")
		return
	}
	st, e := os.Stat(cwd)
	if e != nil {
		err = e
		return
	}
	if !st.IsDir() {
		err = errors.New("terminal working directory is not a directory")
	}
	return
}

const (
	tailLines = 6
	// justEnded is how long a finished job's row still carries its tail, so
	// a watcher sees the last lines the job wrote.
	justEnded = time.Minute
	// logChunk is the most bytes one ShellLogRange call returns.
	logChunk = 256 << 10
)

// ShellStore selects the shell store beside a session store root.
func ShellStore(root string) *shells.Store {
	if root == "" {
		return shells.DefaultStore()
	}
	return &shells.Store{Root: filepath.Join(filepath.Dir(root), "shells")}
}

// ShellJobs includes running commands and recently completed commands.
func (s *Service) ShellJobs(roomID, before string, limit int) (listpage.Page[ShellJob], error) {
	rec, err := s.options.Rooms.Resolve(roomID)
	if err != nil {
		return listpage.Page[ShellJob]{}, err
	}
	store := ShellStore(s.options.SessionRoot)
	list, err := store.ListRoom(rec.ID)
	if err != nil {
		return listpage.Page[ShellJob]{}, err
	}
	out := []ShellJob{}
	for _, in := range list {
		out = append(out, ShellJobRow(store, in))
	}
	return ShellJobPage(out, before, limit)
}

// StopShell stops one of a room's shell jobs: SIGTERM to its process group,
// SIGKILL after 5 s. The web app calls it for the owner. Agents use dyna
// shell stop, which checks that they own the shell.
func (s *Service) StopShell(ctx context.Context, roomID, id string) (ShellJob, error) {
	if authority.Of(ctx).Role != authority.Owner {
		return ShellJob{}, errors.New("only the owner stops a shell through the engine; agents use dyna shell stop")
	}
	rec, err := s.options.Rooms.Resolve(roomID)
	if err != nil {
		return ShellJob{}, err
	}
	store := ShellStore(s.options.SessionRoot)
	in, err := store.Info(id)
	if err != nil {
		return ShellJob{}, err
	}
	if in.RoomID != rec.ID {
		return ShellJob{}, fmt.Errorf("shell %s is not in room %s", id, rec.Name)
	}
	in, err = store.Stop(in.ID, "owner")
	if err != nil {
		return ShellJob{}, err
	}
	return ShellJobRow(store, in), nil
}

// ShellJobLive reports whether a job's row can change with no database
// write: an open job's state and tail, and a just-ended job's tail, which
// it drops once justEnded has passed.
func ShellJobLive(in shells.Info) bool {
	return in.Exit == nil || Clock().Sub(in.Exit.Ended) < justEnded
}

// ShellJobRow is the canonical list and live-event row for one command. It
// carries the tail while the job is live.
func ShellJobRow(store *shells.Store, in shells.Info) ShellJob {
	return shellJob(store, in, ShellJobLive(in))
}

// ShellJobBlock is the row with its tail, for a terminal block in a transcript.
func ShellJobBlock(store *shells.Store, in shells.Info) ShellJob {
	return shellJob(store, in, true)
}

func shellJob(store *shells.Store, in shells.Info, withTail bool) ShellJob {
	job := ShellJob{ID: in.ID, Owner: in.Owner, Command: in.Cmd, Name: in.Name, Cwd: in.Cwd, State: in.Status, Started: in.Started,
		StopBy: in.StopBy, BackgroundAt: in.BackgroundAt, OutputLines: in.OutputLines}
	if in.Exit != nil {
		job.Code, job.Signal = in.Exit.Code, in.Exit.Signal
		job.Ended = &in.Exit.Ended
	}
	if withTail {
		if path, err := store.OutputPath(in.ID); err == nil {
			if st, err := os.Stat(path); err == nil {
				job.OutputBytes = st.Size()
			} else {
				job.LogPruned = errors.Is(err, os.ErrNotExist)
			}
		}
		if text, err := store.Tail(in.ID, tailLines); err == nil {
			job.Tail = plainLines(text)
		}
	}
	return job
}

// plainLines drops escape codes, and keeps what a line shows after its last
// carriage return, as a terminal would for a progress bar. A CRLF line end is
// a line end, not a carriage return.
func plainLines(text string) string {
	lines := strings.Split(strings.ReplaceAll(ansi.Strip(text), "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = line[strings.LastIndex(line, "\r")+1:]
	}
	return strings.Join(lines, "\n")
}

// ShellJobPage applies the shared first-page cutoff and older cursor.
func ShellJobPage(items []ShellJob, before string, limit int) (listpage.Page[ShellJob], error) {
	return listpage.Select(items, before, limit, Clock(), ShellJobMeta)
}

// Clock is the time ShellJobPage pages against. Tests replace it.
var Clock = time.Now

// ShellJobMeta is what places a row in a page.
func ShellJobMeta(j ShellJob) listpage.Meta {
	at := j.Started
	if j.Ended != nil {
		at = *j.Ended
	}
	return listpage.Meta{ID: j.ID, At: at, Open: j.State == "running" || j.State == "starting"}
}

// ShellLog returns at most 2000 lines and the store's last 256 KiB window.
// ANSI control sequences are removed for a plain text log viewer.
func (s *Service) ShellLog(id string, lines int) (ShellLog, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	store := ShellStore(s.options.SessionRoot)
	if _, err := store.ReadMeta(id); err != nil {
		return ShellLog{}, err
	}
	text, err := store.Tail(id, lines)
	if err != nil {
		return ShellLog{}, err
	}
	p, err := store.OutputPath(id)
	if err != nil {
		return ShellLog{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return ShellLog{}, err
	}
	return ShellLog{Text: ansi.Strip(text), Truncated: st.Size() > int64(len(text)+1)}, nil
}

// lastRune is the bytes from the start of buf's last character, which may be
// cut short.
func lastRune(buf []byte) []byte {
	for i := len(buf) - 1; i >= 0 && i >= len(buf)-utf8.UTFMax; i-- {
		if utf8.RuneStart(buf[i]) {
			return buf[i:]
		}
	}
	return nil
}

// ShellLogRange reads the log forward from a byte offset, stopping at a line
// end when more follows. The text is raw log text: it may hold escape codes
// and carriage returns, so chunks join exactly. A chunk never ends inside a
// character, and the reader cleans the joined text.
func (s *Service) ShellLogRange(id string, offset int64, limit int) (ShellLogChunk, error) {
	store := ShellStore(s.options.SessionRoot)
	if _, err := store.ReadMeta(id); err != nil {
		return ShellLogChunk{}, err
	}
	path, err := store.OutputPath(id)
	if err != nil {
		return ShellLogChunk{}, err
	}
	return readLogChunk(path, offset, limit)
}

// readLogChunk is ShellLogRange for the log file at path.
func readLogChunk(path string, offset int64, limit int) (ShellLogChunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return ShellLogChunk{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ShellLogChunk{}, err
	}
	if limit <= 0 || limit > logChunk {
		limit = logChunk
	}
	offset = max(offset, 0)
	buf := make([]byte, max(min(int64(limit), st.Size()-offset), 0))
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return ShellLogChunk{}, err
	}
	buf = buf[:n]
	if offset+int64(n) < st.Size() {
		if cut := bytes.LastIndexByte(buf, '\n'); cut >= 0 {
			buf = buf[:cut+1]
		} else {
			// A line longer than the chunk: read on to the end of its last
			// character, so the cut never falls inside one. At most 3 bytes: output
			// that is not UTF-8 is cut as it comes. Next stays a raw byte offset,
			// and a chunk always moves the reader forward.
			for extra, more := make([]byte, 1), 0; !utf8.FullRune(lastRune(buf)) && more < utf8.UTFMax-1 && int64(len(buf))+offset < st.Size(); more++ {
				if _, err := f.ReadAt(extra, offset+int64(len(buf))); err != nil {
					return ShellLogChunk{}, err
				}
				buf = append(buf, extra...)
			}
		}
	}
	return ShellLogChunk{Text: string(buf), Next: offset + int64(len(buf)), Size: st.Size()}, nil
}
