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
		if rule == nil || rule.ID != id || strings.ContainsAny(id, "\r\n)") || id == "" {
			return "", fmt.Errorf("invalid source rule ID")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out strings.Builder
	for _, id := range ids {
		lines := strings.Split(set.Rules[id].Text, "\n")
		fmt.Fprintf(&out, "- (%s) %s\n", id, lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(&out, "  %s\n", line)
		}
	}
	return out.String(), nil
}

// verifyInstructions compares a parsed document to a separate SQL source read.
func verifyInstructions(db *sql.DB, room, text string) error {
	rows, err := db.Query(`SELECT json_extract(value,'$.id'), json_extract(value,'$.text')
FROM goal_rooms, json_each(goal_rooms.body,'$.rules') WHERE room=?`, room)
	if err != nil {
		return err
	}
	defer rows.Close()
	source := make(map[string]string)
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			return err
		}
		if _, exists := source[id]; exists {
			return fmt.Errorf("duplicate source rule ID")
		}
		source[id] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if text == "" {
		if len(source) != 0 {
			return fmt.Errorf("instruction rules differ from source")
		}
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		return fmt.Errorf("unterminated instruction document")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	seen := make(map[string]bool)
	last := ""
	for i := 0; i < len(lines); {
		line := lines[i]
		if !strings.HasPrefix(line, "- (") {
			return fmt.Errorf("invalid instruction list item")
		}
		id, first, ok := strings.Cut(strings.TrimPrefix(line, "- ("), ") ")
		if !ok || id == "" || (last != "" && id <= last) || seen[id] {
			return fmt.Errorf("invalid instruction rule ID")
		}
		last = id
		i++
		parts := []string{first}
		for i < len(lines) && strings.HasPrefix(lines[i], "  ") {
			parts = append(parts, strings.TrimPrefix(lines[i], "  "))
			i++
		}
		value, exists := source[id]
		if !exists || strings.Join(parts, "\n") != value {
			return fmt.Errorf("instruction rule differs from source")
		}
		seen[id] = true
	}
	if len(seen) != len(source) {
		return fmt.Errorf("instruction rules differ from source")
	}
	return nil
}
