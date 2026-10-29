package sessionlife

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Instruction snapshots are large and shared by many sessions. The hot
// session row keeps their hashes; this table stores each distinct JSON value
// once. The hash is a content address, not a secret or an integrity boundary.
const instructionBlobSchema = "CREATE TABLE IF NOT EXISTS session_instruction_blobs (\n" +
	" hash TEXT PRIMARY KEY,\n" +
	" body TEXT NOT NULL CHECK(json_valid(body))\n" +
	") WITHOUT ROWID;"

var instructionFields = [2]string{"instructions", "instructionsKnown"}

const instructionReadProjection = "instructions_hash, instructions_known_hash,\n" +
	" (SELECT body FROM session_instruction_blobs WHERE hash=session_records.instructions_hash),\n" +
	" (SELECT body FROM session_instruction_blobs WHERE hash=session_records.instructions_known_hash)"

type instructionWrite struct {
	hot    []byte
	hashes [2]string
	bodies [2][]byte
}

type instructionSpan struct {
	field, start, end int
}

type instructionRead struct {
	hashes [2]string
	bodies [2]sql.NullString
}

// scanInstructionRead keeps the projection column order beside its destinations.
func scanInstructionRead(row interface{ Scan(...any) error }, dest ...any) (read instructionRead, err error) {
	dest = append(dest, &read.hashes[0], &read.hashes[1], &read.bodies[0], &read.bodies[1])
	err = row.Scan(dest...)
	return
}

func instructionHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// instructionSpans finds top-level instruction values without changing any
// other byte. InputOffset and the raw value locate the exact replacement span.
func instructionSpans(raw []byte) ([]instructionSpan, error) {
	if !isObject(raw) {
		return nil, fmt.Errorf("%w: session record is not a JSON object", ErrRecord)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var spans []instructionSpan
	seen := [2]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%w: session record has a non-string key", ErrRecord)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		end := int(decoder.InputOffset())
		start := end - len(value)
		for i, field := range instructionFields {
			if name != field {
				continue
			}
			if seen[i] {
				return nil, fmt.Errorf("%w: session record repeats %s", ErrRecord, field)
			}
			seen[i] = true
			spans = append(spans, instructionSpan{field: i, start: start, end: end})
		}
	}
	_, err := decoder.Token()
	return spans, err
}

func replaceInstructionSpans(raw []byte, spans []instructionSpan, bodies [2][]byte) []byte {
	var out bytes.Buffer
	last := 0
	for _, span := range spans {
		out.Write(raw[last:span.start])
		out.Write(bodies[span.field])
		last = span.end
	}
	out.Write(raw[last:])
	return out.Bytes()
}

// splitInstructions replaces each instruction value with null in its original
// position. Joining the blob bytes back into that position reproduces the
// exact input, including field order, unknown fields and insignificant space.
func splitInstructions(raw []byte) (instructionWrite, error) {
	var out instructionWrite
	spans, err := instructionSpans(raw)
	if err != nil {
		return out, err
	}
	nulls := [2][]byte{[]byte("null"), []byte("null")}
	for _, span := range spans {
		body := append([]byte(nil), raw[span.start:span.end]...)
		out.bodies[span.field] = body
		out.hashes[span.field] = instructionHash(body)
	}
	out.hot = replaceInstructionSpans(raw, spans, nulls)
	return out, nil
}

// putInstructionBlob stores body once. A matching hash with different bytes
// is refused rather than silently associating a session with the wrong text.
func putInstructionBlob(conn *sql.Conn, hash string, body []byte) error {
	if hash == "" {
		return nil
	}
	if instructionHash(body) != hash {
		return fmt.Errorf("%w: instruction hash does not match its body", ErrRecord)
	}
	var stored string
	err := conn.QueryRowContext(context.Background(), "SELECT body FROM session_instruction_blobs WHERE hash=?", hash).Scan(&stored)
	if err == nil {
		if stored != string(body) {
			return fmt.Errorf("%w: instruction hash collision", ErrRecord)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = conn.ExecContext(context.Background(), "INSERT INTO session_instruction_blobs(hash,body) VALUES(?,?)", hash, string(body))
	return err
}

func (w instructionWrite) store(conn *sql.Conn) error {
	for i := range w.hashes {
		if err := putInstructionBlob(conn, w.hashes[i], w.bodies[i]); err != nil {
			return err
		}
	}
	return nil
}

func readInstructionHashes(conn *sql.Conn, id string) ([2]string, error) {
	var hashes [2]string
	err := conn.QueryRowContext(context.Background(), "SELECT instructions_hash,instructions_known_hash FROM session_records WHERE id=?", id).Scan(&hashes[0], &hashes[1])
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return hashes, err
}

// deleteUnreferencedInstructionBlobs runs after a reference change in the
// same write transaction. Other sessions keep their shared values, and a
// conflicting insert that does nothing cannot delete the existing references.
func deleteUnreferencedInstructionBlobs(conn *sql.Conn, old, new [2]string) error {
	if old == new {
		return nil
	}
	_, err := conn.ExecContext(context.Background(), `DELETE FROM session_instruction_blobs
WHERE hash IN (?,?) AND hash NOT IN (?,?)
AND NOT EXISTS (
 SELECT 1 FROM session_records
 WHERE instructions_hash=session_instruction_blobs.hash
 OR instructions_known_hash=session_instruction_blobs.hash
)`, old[0], old[1], new[0], new[1])
	return err
}

// join reconstructs the exact full record from its hot row and blobs.
func (r instructionRead) join(hot []byte) ([]byte, error) {
	spans, err := instructionSpans(hot)
	if err != nil {
		return nil, err
	}
	var bodies [2][]byte
	for i, hash := range r.hashes {
		if hash == "" {
			continue
		}
		if !r.bodies[i].Valid {
			return nil, fmt.Errorf("%w: session record is missing an instruction blob", ErrRecord)
		}
		bodies[i] = []byte(r.bodies[i].String)
	}
	present := [2]bool{}
	replacements := make([]instructionSpan, 0, len(spans))
	for _, span := range spans {
		if r.hashes[span.field] == "" {
			continue
		}
		present[span.field] = true
		if len(bodies[span.field]) == 0 {
			return nil, fmt.Errorf("%w: session record has an invalid instruction blob", ErrRecord)
		}
		replacements = append(replacements, span)
	}
	for i, hash := range r.hashes {
		if hash != "" && !present[i] {
			return nil, fmt.Errorf("%w: session record is missing an instruction placeholder", ErrRecord)
		}
	}
	return replaceInstructionSpans(hot, replacements, bodies), nil
}

// migrateInstructionBlobs is schema version 5. It runs in the same
// transaction as the schema change and does not advance record revisions.
func migrateInstructionBlobs(ctx context.Context, conn *sql.Conn) error {
	for _, column := range []string{"instructions_hash", "instructions_known_hash"} {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE session_records ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, instructionBlobSchema); err != nil {
		return err
	}
	type row struct{ id, meta string }
	rows, err := conn.QueryContext(ctx, "SELECT id,meta FROM session_records ORDER BY id")
	if err != nil {
		return err
	}
	var records []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.meta); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, r := range records {
		raw := []byte(r.meta)
		if !isObject(raw) {
			// Keep damaged rows fail-closed with their existing digest.
			continue
		}
		w, err := splitInstructions(raw)
		if err != nil {
			return err
		}
		if err := w.store(conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "UPDATE session_records SET meta=?,instructions_hash=?,instructions_known_hash=? WHERE id=?", w.hot, w.hashes[0], w.hashes[1], r.id); err != nil {
			return err
		}
	}
	return nil
}
