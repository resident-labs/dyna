package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"dyna-agent/internal/goals"
)

// renderInstructions keeps every source text line, including blank lines.
func renderInstructions(db *sql.DB, room string) (string, error) {
	var body []byte
	if err := db.QueryRow("SELECT body FROM goal_rooms WHERE room=?", room).Scan(&body); errors.Is(err, sql.ErrNoRows) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	var set goals.RuleSet
	if err := json.Unmarshal(body, &set); err != nil {
		return "", err
	}
	ids := make([]string, 0, len(set.Rules))
	for id, rule := range set.Rules {
		if rule == nil || rule.ID != id || id == "" {
			return "", fmt.Errorf("invalid source rule ID")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "", nil
	}
	var out strings.Builder
	out.WriteString("# Instructions\n\n")
	for _, id := range ids {
		lines := strings.Split(set.Rules[id].Text, "\n")
		fmt.Fprintf(&out, "- %s\n", lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(&out, "  %s\n", line)
		}
	}
	return out.String(), nil
}

// verifyInstructions compares parsed list items to an independent, ordered SQL read.
func verifyInstructions(db *sql.DB, room, text string) error {
	rows, err := db.Query(`SELECT key, json_extract(value,'$.id'), json_extract(value,'$.text')
FROM goal_rooms, json_each(goal_rooms.body,'$.rules') WHERE room=? ORDER BY key`, room)
	if err != nil {
		return err
	}
	defer rows.Close()
	var source []string
	for rows.Next() {
		var key, id, value string
		if err := rows.Scan(&key, &id, &value); err != nil {
			return err
		}
		if key == "" || key != id {
			return fmt.Errorf("invalid source rule ID")
		}
		source = append(source, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(source) == 0 {
		if text != "" {
			return fmt.Errorf("instruction rules differ from source")
		}
		return nil
	}
	if !strings.HasPrefix(text, "# Instructions\n\n") || !strings.HasSuffix(text, "\n") {
		return fmt.Errorf("invalid instruction document")
	}
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(text, "# Instructions\n\n"), "\n"), "\n")
	index := 0
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "- ") {
			return fmt.Errorf("invalid instruction list item")
		}
		parts := []string{strings.TrimPrefix(lines[i], "- ")}
		i++
		for i < len(lines) && strings.HasPrefix(lines[i], "  ") {
			parts = append(parts, strings.TrimPrefix(lines[i], "  "))
			i++
		}
		if index >= len(source) || strings.Join(parts, "\n") != source[index] {
			return fmt.Errorf("instruction rule differs from source")
		}
		index++
	}
	if index != len(source) {
		return fmt.Errorf("instruction rules differ from source")
	}
	return nil
}
