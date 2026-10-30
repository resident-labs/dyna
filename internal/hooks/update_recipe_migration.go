package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	"dyna-agent/internal/database"
)

// MigrateUpdateRecipe imports one unmodified recipe atomically. Multiple
// recipes remain hooks, because one source setting cannot represent them.
func MigrateUpdateRecipe(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "updater.service", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, `CREATE TABLE updater_service (id INTEGER PRIMARY KEY CHECK(id=1), source TEXT NOT NULL, enabled INTEGER NOT NULL, every_minutes INTEGER NOT NULL, multiple_hooks INTEGER NOT NULL, tool_path TEXT NOT NULL DEFAULT '', next_due TEXT NOT NULL DEFAULT '')`); err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx, `SELECT repo, body FROM hook_documents WHERE repo <> ? ORDER BY repo`, globalKey)
		if err != nil {
			return err
		}
		type match struct {
			key    string
			doc    map[string]json.RawMessage
			hooks  []json.RawMessage
			index  int
			hook   Hook
			source string
			tools  string
		}
		var matches []match
		for rows.Next() {
			var key string
			var body []byte
			if err := rows.Scan(&key, &body); err != nil {
				rows.Close()
				return err
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(body, &doc); err != nil {
				rows.Close()
				return err
			}
			var list []json.RawMessage
			if raw := doc["hooks"]; len(raw) != 0 {
				if err := json.Unmarshal(raw, &list); err != nil {
					rows.Close()
					return err
				}
			}
			for i, raw := range list {
				var h Hook
				if err := json.Unmarshal(raw, &h); err != nil {
					rows.Close()
					return err
				}
				if source, tools, ok := updateRecipeSource(h); ok {
					matches = append(matches, match{key, doc, list, i, h, source, tools})
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(matches) != 1 {
			_, err := conn.ExecContext(ctx, `INSERT INTO updater_service(id,source,enabled,every_minutes,multiple_hooks) VALUES(1,'',0,60,?)`, len(matches) > 1)
			return err
		}
		m := matches[0]
		interval := m.hook.EveryMinutes
		if interval == 0 {
			interval = 60
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO updater_service(id,source,enabled,every_minutes,multiple_hooks,tool_path) VALUES(1,?,?,?,0,?)`, m.source, !m.hook.Disabled, interval, m.tools); err != nil {
			return err
		}
		m.hooks = append(m.hooks[:m.index], m.hooks[m.index+1:]...)
		m.doc["hooks"], err = json.Marshal(m.hooks)
		if err != nil {
			return err
		}
		body, err := json.Marshal(m.doc)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `UPDATE hook_documents SET body=? WHERE repo=?`, body, m.key)
		return err
	})
}

// Recipe words were always single-quoted by shellquote. Reject all other shell
// syntax so an owner-edited command can never be removed by the migration.
var updateRecipeCommand = regexp.MustCompile(`^('(?:[^']|'\\'')*') git hooks _install --main ('(?:[^']|'\\'')*') --name ('(?:[^']|'\\'')*') --tool-path ('(?:[^']|'\\'')*')$`)

func updateRecipeSource(h Hook) (string, string, bool) {
	if h.Label != "Update Dyna" {
		return "", "", false
	}
	words := updateRecipeCommand.FindStringSubmatch(strings.TrimSpace(h.Command))
	if len(words) != 5 {
		return "", "", false
	}
	unquote := func(s string) string {
		return strings.ReplaceAll(s[1:len(s)-1], `'\''`, `'`)
	}
	exe, source, name, tools := unquote(words[1]), unquote(words[2]), unquote(words[3]), unquote(words[4])
	if exe == "" || source == "" || tools == "" || name != h.Name {
		return "", "", false
	}
	return source, tools, true
}
