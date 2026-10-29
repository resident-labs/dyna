package shellmcp

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/shells"
)

// outdatedServer serves one shell MCP server whose binary is older than the
// state it serves: every store call fails as a newer schema. Its stdin is a
// real pipe, as a harness's is, so the server can wait for input without
// consuming it. search, when set, is the engine's web search.
type outdatedServer struct {
	t         *testing.T
	writes    *os.File
	replies   *bufio.Reader
	restarted chan struct{}
	served    chan error
}

func startOutdatedServer(t *testing.T, search func(context.Context, callspec.WebSearchRequest) (callspec.WebSearchResponse, error)) *outdatedServer {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), "INSERT INTO meta (key, value) VALUES ('shells.version', '99')")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &outdatedServer{t: t, restarted: make(chan struct{}, 1), served: make(chan error, 1)}
	tools := &Tools{Store: &shells.Store{Root: filepath.Join(dir, "shells")}, Search: search}
	tools.SetRestart(func() { s.restarted <- struct{}{} })
	requests, writes, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reads, answers, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.writes, s.replies = writes, bufio.NewReader(reads)
	go func() { s.served <- Serve(context.Background(), requests, answers, tools) }()
	t.Cleanup(func() { reads.Close() })
	return s
}

func (s *outdatedServer) send(text string) {
	s.t.Helper()
	if _, err := s.writes.WriteString(text); err != nil {
		s.t.Fatal(err)
	}
}

func (s *outdatedServer) reply() string {
	s.t.Helper()
	line, err := s.replies.ReadString('\n')
	if err != nil {
		s.t.Fatal(err)
	}
	return line
}

func (s *outdatedServer) finish() {
	s.t.Helper()
	s.writes.Close()
	if err := <-s.served; err != nil {
		s.t.Fatal(err)
	}
}

func (s *outdatedServer) notRestarted() {
	s.t.Helper()
	select {
	case <-s.restarted:
		s.t.Fatal("the server restarted too early")
	default:
	}
}

const (
	callJobs   = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell_jobs","arguments":{}}}` + "\n"
	callSearch = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"x"}}}` + "\n"
	pingWhole  = `{"jsonrpc":"2.0","id":4,"method":"ping"}` + "\n"
	pingHead   = `{"jsonrpc":"2.0","id":3,"meth`
	pingTail   = `od":"ping"}` + "\n"
)

// A server whose binary is older than the state it serves answers the failing
// call, then restarts on the binary its start path now holds. An installed
// update replaces that binary in place, so the restart lands on the current
// build with stdio inherited, and a schema bump never cuts a running
// session's shells off (D-171).
func TestServeRestartsAnOutdatedServerAfterItsAnswer(t *testing.T) {
	s := startOutdatedServer(t, nil)
	s.send(callJobs)
	if line := s.reply(); !strings.Contains(line, "newer dyna") {
		t.Fatalf("answer missing the schema error: %s", line)
	}
	<-s.restarted
	s.finish()
}

// A restart replaces the process, so it waits for every accepted call to
// answer. A schema-rejected call that finishes while a web search still runs
// must not take the search's response with it.
func TestServeRestartWaitsForCallsStillRunning(t *testing.T) {
	searching, release := make(chan struct{}), make(chan struct{})
	s := startOutdatedServer(t, func(context.Context, callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		close(searching)
		<-release
		return callspec.WebSearchResponse{}, nil
	})
	s.send(callSearch)
	<-searching
	s.send(callJobs)
	if line := s.reply(); !strings.Contains(line, `"id":2`) || !strings.Contains(line, "newer dyna") {
		t.Fatalf("the outdated call answered %q", line)
	}
	s.notRestarted()
	close(release)
	if line := s.reply(); !strings.Contains(line, `"id":1`) {
		t.Fatalf("the search lost its answer: %q", line)
	}
	<-s.restarted
	s.finish()
}

// A request the server has begun to read is never cut off by a restart: the
// new process would see only its tail. The restart waits until the half-read
// request is answered, and only then replaces the process.
func TestServeRestartWaitsForAHalfReadRequest(t *testing.T) {
	searching, release := make(chan struct{}), make(chan struct{})
	s := startOutdatedServer(t, func(context.Context, callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		close(searching)
		<-release
		return callspec.WebSearchResponse{}, nil
	})
	s.send(callSearch)
	<-searching
	s.send(callJobs)
	if line := s.reply(); !strings.Contains(line, `"id":2`) {
		t.Fatalf("the outdated call answered %q", line)
	}
	// A whole request and half of the next one arrive in one write, so the
	// server has read the half once it answers the whole one.
	s.send(pingWhole + pingHead)
	if line := s.reply(); !strings.Contains(line, `"id":4`) {
		t.Fatalf("the whole request answered %q", line)
	}
	close(release)
	if line := s.reply(); !strings.Contains(line, `"id":1`) {
		t.Fatalf("the search lost its answer: %q", line)
	}
	s.notRestarted()
	s.send(pingTail)
	if line := s.reply(); !strings.Contains(line, `"id":3`) {
		t.Fatalf("the split request lost its answer: %q", line)
	}
	<-s.restarted
	s.finish()
}

const callWrite = `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"shell","arguments":{"command":"true","description":"Run true"}}}` + "\n"

// A store the engine migrated past this binary refuses this server's next
// write from inside its transaction, even when the write still fits the old
// tables, and the server then restarts on the current binary (D-468). Only
// the version row advances; no real migration runs.
func TestServeRefusesAWriteToAStoreThatAdvancedPastThisBinary(t *testing.T) {
	dir := t.TempDir()
	if err := shells.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s := &outdatedServer{t: t, restarted: make(chan struct{}, 1), served: make(chan error, 1)}
	tools := &Tools{Store: &shells.Store{Root: filepath.Join(dir, "shells")}}
	tools.SetRestart(func() { s.restarted <- struct{}{} })
	requests, writes, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reads, answers, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.writes, s.replies = writes, bufio.NewReader(reads)
	go func() { s.served <- Serve(context.Background(), requests, answers, tools) }()
	t.Cleanup(func() { reads.Close() })
	s.send(callJobs)
	if line := s.reply(); strings.Contains(line, "newer dyna") {
		t.Fatalf("a current store refused a call: %s", line)
	}
	s.notRestarted()
	// The engine's forward migration moved the store past this build.
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "UPDATE meta SET value='99' WHERE key='shells.version'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// This server is an installed client, not the engine.
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	s.send(callWrite)
	if line := s.reply(); !strings.Contains(line, "newer dyna") {
		t.Fatalf("the write into the advanced store was not refused: %s", line)
	}
	<-s.restarted
	s.finish()
}
