package subagent

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
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
}
