package shellmcp

import (
	"bufio"
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/shells"
)

// A server whose binary is older than the state it serves answers the failing
// call, then restarts on the binary its start path now holds. An installed
// update replaces that binary in place, so the restart lands on the current
// build with stdio inherited, and a schema bump never cuts a running
// session's shells off (D-171).
func TestServeRestartsAnOutdatedServerAfterItsAnswer(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// State a newer dyna wrote, without migrating first: the store must
	// refuse it. The shared handle stays open, as database.Open shares one
	// handle per data folder.
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
	restarted := make(chan struct{}, 1)
	tools := &Tools{Store: &shells.Store{Root: filepath.Join(dir, "shells")}}
	tools.SetRestart(func() { restarted <- struct{}{} })
	requests, clientWrites := io.Pipe()
	clientReads, answers := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(context.Background(), requests, answers, tools) }()
	if _, err := clientWrites.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell_jobs","arguments":{}}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(clientReads).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "newer dyna") {
		t.Fatalf("answer missing the schema error: %s", line)
	}
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not restart after answering the outdated call")
	}
	clientWrites.Close()
	clientReads.Close()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

// A restart replaces the process, so it waits for every accepted call to
// answer. A schema-rejected call that finishes while a web search still runs
// must not take the search's response with it (D-171).
func TestServeRestartWaitsForCallsStillRunning(t *testing.T) {
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
	searching, release := make(chan struct{}), make(chan struct{})
	tools := &Tools{Store: &shells.Store{Root: filepath.Join(dir, "shells")}, Search: func(context.Context, callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
		close(searching)
		<-release
		return callspec.WebSearchResponse{}, nil
	}}
	restarted := make(chan struct{}, 1)
	tools.SetRestart(func() { restarted <- struct{}{} })
	requests, clientWrites := io.Pipe()
	clientReads, answers := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(context.Background(), requests, answers, tools) }()
	reply := bufio.NewReader(clientReads)
	write := func(line string) {
		if _, err := clientWrites.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"x"}}}`)
	<-searching
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell_jobs","arguments":{}}}`)
	line, err := reply.ReadString('\n')
	if err != nil || !strings.Contains(line, `"id":2`) || !strings.Contains(line, "newer dyna") {
		t.Fatalf("the outdated call answered %q (%v)", line, err)
	}
	select {
	case <-restarted:
		t.Fatal("the server restarted while a search was still running")
	default:
	}
	close(release)
	line, err = reply.ReadString('\n')
	if err != nil || !strings.Contains(line, `"id":1`) {
		t.Fatalf("the search lost its answer: %q (%v)", line, err)
	}
	<-restarted
	clientWrites.Close()
	clientReads.Close()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
