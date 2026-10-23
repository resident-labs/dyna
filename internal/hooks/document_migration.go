package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"dyna-agent/internal/shellquote"
)

var quotedRecipeWord = regexp.MustCompile("^'(?:[^']|'\\\\'')*'$")

// migrateDocumentFields patches stored event aliases and built-in commands.
// Other JSON values, including unknown fields, remain intact. Row whitespace
// can change. It runs once in the v3 transaction, without importing files.
// TODO: legacy/shim remove later — converts stored hook aliases and built-in recipes; remove when all supported stores have passed hooks.documents v3 and older upgrades are retired.
func migrateDocumentFields(conn *sql.Conn) error {
	ctx := context.Background()
	rows, err := conn.QueryContext(ctx, "SELECT repo,body FROM hook_documents ORDER BY repo")
	if err != nil {
		return err
	}
	type fieldChange struct {
		key   string
		index int
		field string
		value string
	}
	var changes []fieldChange
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return err
		}
		var d document
		if err := json.Unmarshal(raw, &d); err != nil {
			rows.Close()
			return fmt.Errorf("decode hook document for field migration: %w", err)
		}
		for i, h := range d.Hooks {
			if h.Event == "main-moved" {
				changes = append(changes, fieldChange{key, i, "event", MainUpdated})
			}
			if h.Label != "Update Dyna" || h.Cwd != "main" {
				continue
			}
			// Match the entire old quoted recipe, including this document's
			// repository and hook name. Extra shell syntax is not a recipe.
			args := " hooks _install --main " + shellquote.Quote(d.Repo) + " --name " + shellquote.Quote(h.Name) + " --tool-path "
			exe, toolPath, ok := strings.Cut(h.Command, args)
			if !ok || !strings.HasPrefix(exe, "'/") || !quotedRecipeWord.MatchString(exe) || !strings.HasPrefix(toolPath, "'/") || !quotedRecipeWord.MatchString(toolPath) {
				continue
			}
			changes = append(changes, fieldChange{key, i, "command", exe + " git" + strings.TrimPrefix(h.Command, exe)})
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, change := range changes {
		// Patch only the selected field; do not serialize the typed document.
		path := fmt.Sprintf("$.hooks[%d].%s", change.index, change.field)
		if _, err := conn.ExecContext(ctx, "UPDATE hook_documents SET body=json_set(CAST(body AS TEXT),?,?) WHERE repo=?", path, change.value, change.key); err != nil {
			return err
		}
	}
	return nil
}
