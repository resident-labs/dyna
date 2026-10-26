package state

import (
	"database/sql"
	"encoding/json"
	"testing"
)

// joinedBody returns the record as one document, the shape it had before its
// cold fields moved to pr_record_parts, so the tests of the older migrations
// still read the content those migrations produce.
func joinedBody(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var hot []byte
	if err := db.QueryRow("SELECT body FROM pr_rows WHERE key=?", key).Scan(&hot); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(hot, &doc); err != nil {
		t.Fatal(err)
	}
	record := map[string]json.RawMessage{}
	if raw := doc["record"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query("SELECT part, body FROM pr_record_parts WHERE key=?", key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var part string
		var body []byte
		if err := rows.Scan(&part, &body); err != nil {
			t.Fatal(err)
		}
		if partName(part) == partResponses {
			doc["responses"] = body
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for name, value := range fields {
			record[name] = value
		}
	}
	if _, ok := doc["record"]; ok {
		doc["record"], _ = json.Marshal(record)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
