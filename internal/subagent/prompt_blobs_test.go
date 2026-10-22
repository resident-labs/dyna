package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

func TestSessionDeletionPreservesSharedPromptBlobUntilLastReference(t *testing.T) {
	dataDir := t.TempDir()
	sessions := filepath.Join(dataDir, "sessions")
	t.Cleanup(func() { database.Close(dataDir) })
	first, second := NewID(), NewID()
	for _, id := range []string{first, second} {
		if err := sessionlife.Save(sessions, id, map[string]string{"id": id}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	// Use fixture SQL so this regression runs against the pre-schema implementation.
	_, err = db.Exec(`CREATE TABLE prompt_blobs(hash TEXT PRIMARY KEY, text TEXT NOT NULL);
CREATE TABLE prompt_refs(session TEXT NOT NULL, n INTEGER NOT NULL, hash TEXT NOT NULL, PRIMARY KEY(session,n,hash));
INSERT INTO prompt_blobs VALUES ('shared','shared text'), ('unique','unique text');`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ session, hash string }{{first, "shared"}, {first, "unique"}, {second, "shared"}} {
		if _, err := db.Exec("INSERT INTO prompt_refs VALUES (?,1,?)", row.session, row.hash); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessionlife.Delete(sessions, first); err != nil {
		t.Fatal(err)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT count(*) FROM prompt_refs WHERE session=?", first); n != 0 {
		t.Fatalf("deleted session has %d prompt references", n)
	}
	if n := count("SELECT count(*) FROM prompt_refs WHERE session=? AND hash='shared'", second); n != 1 {
		t.Fatalf("remaining shared references = %d", n)
	}
	if n := count("SELECT count(*) FROM prompt_blobs"); n != 1 {
		t.Fatalf("remaining blobs = %d", n)
	}
	var text string
	if err := db.QueryRow("SELECT text FROM prompt_blobs WHERE hash='shared'").Scan(&text); err != nil || text != "shared text" {
		t.Fatalf("shared text = %q, %v", text, err)
	}
	if err := sessionlife.Delete(sessions, second); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM prompt_refs"); n != 0 {
		t.Fatalf("remaining references = %d", n)
	}
	if n := count("SELECT count(*) FROM prompt_blobs"); n != 0 {
		t.Fatalf("remaining blobs = %d", n)
	}
	// Exercise the staged helpers in the same deletion regression. Ordinary
	// turn readers and writers do not call these helpers until format 3.
	ctx := context.Background()
	write := func(id string, raw []byte) []byte {
		t.Helper()
		var encoded []byte
		err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			var err error
			encoded, err = storeTurnPrompts(ctx, conn, id, 1, raw)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	shared := []byte(`{"prompt":"shared text","heldPrompt":"unique text","other":42}`)
	encoded := write(first, shared)
	other := write(second, []byte(`{"prompt":"shared text"}`))
	if n := count("SELECT count(*) FROM prompt_blobs"); n != 2 {
		t.Fatalf("deduplicated blobs = %d", n)
	}
	read := func(id string, raw []byte) ([]byte, error) {
		var resolved []byte
		err := state.For(db).Read(ctx, func(r state.Reader) error {
			var err error
			resolved, err = resolveRowPrompts(ctx, r, id, 1, raw)
			return err
		})
		return resolved, err
	}
	resolved, err := read(first, encoded)
	var fields map[string]any
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resolved, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["prompt"] != "shared text" || fields["heldPrompt"] != "unique text" || fields["other"] != float64(42) {
		t.Fatalf("resolved fields = %v", fields)
	}
	// A hash in another session is still ordinary inline text.
	inline, err := read(NewID(), other)
	if err != nil || string(inline) != string(other) {
		t.Fatalf("inline hash changed: %s, %v", inline, err)
	}
	abort := errors.New("rollback")
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := storeTurnPrompts(ctx, conn, first, 1, []byte(`{"prompt":"rolled back"}`)); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM prompt_blobs WHERE text='rolled back'"); n != 0 {
		t.Fatal("rollback left a blob")
	}
	resolved, err = read(first, encoded)
	if err != nil || !strings.Contains(string(resolved), "unique text") {
		t.Fatalf("rollback lost old text: %s, %v", resolved, err)
	}
	write(first, []byte(`{"prompt":"replacement"}`))
	if n := count("SELECT count(*) FROM prompt_blobs WHERE text='unique text'"); n != 0 {
		t.Fatal("replacement left unused text")
	}
	if _, err := read(second, other); err != nil {
		t.Fatalf("replacement lost shared text: %v", err)
	}
	if _, err := db.Exec("DELETE FROM prompt_blobs WHERE text='shared text'"); err != nil {
		t.Fatal(err)
	}
	if _, err := read(second, other); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("missing blob error = %v", err)
	}

}
