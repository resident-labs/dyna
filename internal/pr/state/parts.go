package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// A stored PR record is the hot row in pr_rows plus cold parts in
// pr_record_parts. A watcher poll changes the hot fields (checks, polling,
// state) every few seconds; the notices, the review history and the saved
// HTTP responses change on events. Parts keep a poll write near 3 KB instead
// of the 26 KB whole record, and a write rewrites only the parts that changed.
type partName string

const (
	partNotices   partName = "notices"
	partDetail    partName = "detail"
	partResponses partName = "responses"
)

// partFields names the top-level record fields that each part stores.
var partFields = map[partName][]string{
	partNotices: {"notifications"},
	partDetail: {"reviewRounds", "reviewHistory", "summary", "botReviews", "heldComments",
		"admittedComments", "commentDecisions", "pendingDroppedComments"},
}

const partsSchema = `CREATE TABLE IF NOT EXISTS pr_record_parts (
 key TEXT NOT NULL, part TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)),
 PRIMARY KEY(key, part)
) WITHOUT ROWID;`

// storedParts is a record's cold parts as JSON. A part with no data is absent.
type storedParts map[partName][]byte

// splitStored encodes d as the hot body and its cold parts.
func splitStored(d diskRecord) ([]byte, storedParts, error) {
	full, err := marshalStored(d)
	if err != nil {
		return nil, nil, err
	}
	return splitEncoded(full)
}

// splitEncoded splits an encoded diskRecord. The hot body keeps the
// {"record":{...}} shape with the cold fields removed.
func splitEncoded(full []byte) ([]byte, storedParts, error) {
	// The envelope and the record may hold fields this version does not know;
	// they stay in the hot body unchanged.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(full, &doc); err != nil {
		return nil, nil, err
	}
	parts := storedParts{}
	if responses, ok := doc["responses"]; ok {
		parts[partResponses] = responses
		delete(doc, "responses")
	}
	var record map[string]json.RawMessage
	if len(doc["record"]) > 0 {
		if err := json.Unmarshal(doc["record"], &record); err != nil {
			return nil, nil, err
		}
	}
	for name, fields := range partFields {
		cold := map[string]json.RawMessage{}
		for _, field := range fields {
			if value, ok := record[field]; ok {
				cold[field] = value
				delete(record, field)
			}
		}
		if len(cold) > 0 {
			body, err := json.Marshal(cold)
			if err != nil {
				return nil, nil, err
			}
			parts[name] = body
		}
	}
	if doc["record"] != nil && string(doc["record"]) != "null" {
		hotRecord, err := json.Marshal(record)
		if err != nil {
			return nil, nil, err
		}
		doc["record"] = hotRecord
	}
	body, err := json.Marshal(doc)
	return body, parts, err
}

// joinStored decodes a hot body and its parts into one record.
func joinStored(hot []byte, parts storedParts) (diskRecord, error) {
	var record diskRecord
	if err := unmarshalRecord(hot, &record); err != nil {
		return diskRecord{}, err
	}
	for name, body := range parts {
		switch {
		case name == partResponses:
			if err := json.Unmarshal(body, &record.Responses); err != nil {
				return diskRecord{}, fmt.Errorf("%s: %w", name, err)
			}
		case record.Record == nil:
			// A row without a record carries no cold fields either.
		default:
			if err := json.Unmarshal(body, record.Record); err != nil {
				return diskRecord{}, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return record, nil
}

// partWrites lists the part rows to write and to remove so that the stored
// parts of a record become next. before is nil for a new record.
func partWrites(before, next storedParts) (upsert storedParts, remove []partName) {
	upsert = storedParts{}
	for name, body := range next {
		if old, ok := before[name]; !ok || string(old) != string(body) {
			upsert[name] = body
		}
	}
	for name := range before {
		if _, kept := next[name]; !kept {
			remove = append(remove, name)
		}
	}
	slices.Sort(remove)
	return upsert, remove
}

// migrateRecordParts moves the cold fields of every stored record into
// pr_record_parts, once. The old whole-body shape is not read afterwards.
func migrateRecordParts(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, partsSchema); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, "SELECT key, body FROM pr_records")
	if err != nil {
		return err
	}
	type split struct {
		key   string
		hot   []byte
		parts storedParts
	}
	var splits []split
	for rows.Next() {
		var key string
		var body []byte
		if err := rows.Scan(&key, &body); err != nil {
			rows.Close()
			return err
		}
		hot, parts, err := splitEncoded(body)
		if err != nil {
			rows.Close()
			return fmt.Errorf("invalid PR record %s: %w", key, err)
		}
		splits = append(splits, split{key, hot, parts})
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	// Revisions stay as they are: no reader holds a snapshot while the engine
	// migrates, and the rows' content does not change.
	for _, s := range splits {
		if _, err := conn.ExecContext(ctx, "UPDATE pr_records SET body=? WHERE key=?", s.hot, s.key); err != nil {
			return err
		}
		for name, body := range s.parts {
			if _, err := conn.ExecContext(ctx, "INSERT INTO pr_record_parts(key,part,body) VALUES(?,?,?)", s.key, string(name), body); err != nil {
				return err
			}
		}
	}
	// Renaming the table is the fence for processes that already passed the
	// version check: their next read or write of pr_records fails with "no such
	// table" instead of returning half a record or writing cold fields back.
	_, err = conn.ExecContext(ctx, "ALTER TABLE pr_records RENAME TO pr_rows")
	return err
}
