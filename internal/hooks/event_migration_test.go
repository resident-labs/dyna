package hooks

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/shellquote"
	statewrite "dyna-agent/internal/state"
)

func TestHookDocumentMigrationConvertsAliasesAndRecipesOnce(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := statewrite.MigrateMirrors(dir); err != nil {
		t.Fatal(err)
	}
	repo, exe, name := "/repo dir/dyna's", "/bin dir/dyna's", "self-update"
	command := shellquote.Quote(exe) + " hooks _install --main " + shellquote.Quote(repo) + " --name " + shellquote.Quote(name) + " --tool-path " + shellquote.Quote("/tools dir:/usr/bin")
	base := map[string]any{"name": name, "label": "Update Dyna", "event": "main-moved", "command": command, "cwd": "main", "timeout": "25m", "onFail": "alert", "addedBy": "owner", "addedAt": "2026-10-01T01:02:03Z", "editedBy": "owner", "editedAt": "2026-10-02T01:02:03Z", "everyMinutes": 17, "future": json.Number("18446744073709551617")}
	hooks := []any{base}
	for field, value := range map[string]any{"label": "Owner script", "cwd": "/owner/worktree", "name": "other", "command": command + "; owner-script"} {
		custom := map[string]any{}
		for k, v := range base {
			custom[k] = v
		}
		custom[field] = value
		custom["event"] = Schedule
		custom["scope"] = ScopeRepository
		hooks = append(hooks, custom)
	}
	original := map[string]any{"repo": repo, "hooks": hooks, "future": map[string]any{"large": json.Number("9007199254740993")}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "repos", "saved", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "hooks.documents", 2, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(t.Context(), documentsSchema); err != nil {
			return err
		}
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO hook_documents(repo,body) VALUES(?,?)", "saved", raw); err != nil {
			return err
		}
		_, err := conn.ExecContext(t.Context(), "INSERT INTO state_mirrors(key,version,pending) VALUES(?,?,?)", "hooks/documents/saved", "owner-stamp", "pending-stamp")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		t.Helper()
		var body []byte
		if err := db.QueryRow("SELECT body FROM hook_documents WHERE repo='saved'").Scan(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	decode := func(raw []byte) any {
		t.Helper()
		var value any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if err := MigrateDocuments(dir); err != nil {
		t.Fatal(err)
	}
	base["event"] = MainUpdated
	base["scope"] = ScopeRepository
	base["command"] = shellquote.Quote(exe) + " git hooks _install --main " + shellquote.Quote(repo) + " --name " + shellquote.Quote(name) + " --tool-path " + shellquote.Quote("/tools dir:/usr/bin")
	expected, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(decode(got), decode(expected)) {
		t.Fatalf("migrated document = %s; want %s", got, expected)
	}
	var version int
	if err := db.QueryRow("SELECT value FROM meta WHERE key='hooks.documents'").Scan(&version); err != nil || version != 4 {
		t.Fatalf("version = %d, %v", version, err)
	}
	var stamp, pending string
	if err := db.QueryRow("SELECT version,pending FROM state_mirrors WHERE key='hooks/documents/saved'").Scan(&stamp, &pending); err != nil || stamp != "owner-stamp" || pending != "pending-stamp" {
		t.Fatalf("mirror stamps = %s, %s, %v", stamp, pending, err)
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("source changed: %s, %v", got, err)
	}
	// Later owner commands are outside the one-time migration, even if they
	// contain the old route or alias spelling.
	if _, err := db.Exec("UPDATE hook_documents SET body=json_set(CAST(body AS TEXT),'$.hooks[0].command',?,'$.hooks[0].event',?) WHERE repo='saved'", command+"; owner-edit", "main-moved"); err != nil {
		t.Fatal(err)
	}
	edited := read()
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(dir); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); !bytes.Equal(got, edited) {
		t.Fatalf("owner edit changed after reopen: %s", got)
	}
}
