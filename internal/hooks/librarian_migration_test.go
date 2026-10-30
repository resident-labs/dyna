package hooks

import (
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
)

func TestLibrarianHookMigration(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "hooks.documents", 4, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(t.Context(), documentsSchema); err != nil {
			return err
		}
		return writeDocument(t.Context(), conn, "repo", document{Repo: "/repo", Hooks: []Hook{
			{Name: "librarian-main"}, {Name: "librarian-merged", Command: "dyna library wake --repo /repo"},
			{Name: "librarian-batch", Command: "custom"}, {Name: "other"},
		}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(dir); err != nil {
		t.Fatal(err)
	}
	d, err := readDocument(t.Context(), db, "repo")
	if err != nil || len(d.Hooks) != 2 || d.Hooks[0].Name != "librarian-batch" || d.Hooks[1].Name != "other" {
		t.Fatalf("hooks = %+v, %v", d.Hooks, err)
	}
}
