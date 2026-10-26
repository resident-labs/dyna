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
