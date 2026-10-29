package sessionlife

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Legacy layout of schema version 5: session_records stored hot rows whose
// two instruction fields were null placeholders, with hash columns pointing
// at session_instruction_blobs rows. Version 6 restored every instruction
// text inline and removed the layout; this file only reads that layout, to
// migrate it away.
const instructionReadProjection = "instructions_hash, instructions_known_hash,\n" +
	" (SELECT body FROM session_instruction_blobs WHERE hash=session_records.instructions_hash),\n" +
	" (SELECT body FROM session_instruction_blobs WHERE hash=session_records.instructions_known_hash)"

var instructionFields = [2]string{"instructions", "instructionsKnown"}

type instructionSpan struct {
	field, start, end int
}

type instructionRead struct {
	hashes [2]string
	bodies [2]sql.NullString
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

// restoreInstructionBodies is schema version 6, the revert of the version 5
// blob split. It runs in the same transaction as the schema change: every
// row is re-joined with its blobs byte-for-byte (field order, unknown
// fields and spacing preserved) and written back as text, revisions do not
// advance, and a row whose blobs are gone fails the migration instead of
// silently losing its instructions. The blob table and hash columns drop
// only after every row is restored; the version 5 import had also left some
// metas with the blob storage class, which this rewrites as text. A store
// that never ran version 5 has no blob table and keeps its rows untouched.
func restoreInstructionBodies(ctx context.Context, conn *sql.Conn) error {
	var blobs int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_instruction_blobs'").Scan(&blobs); err != nil {
		return err
	}
	if blobs == 0 {
		return nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT id,meta,"+instructionReadProjection+" FROM session_records ORDER BY id")
	if err != nil {
		return err
	}
	type row struct {
		id           string
		meta         string
		instructions instructionRead
	}
	var records []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.meta, &r.instructions.hashes[0], &r.instructions.hashes[1], &r.instructions.bodies[0], &r.instructions.bodies[1]); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, r := range records {
		raw, err := r.instructions.join([]byte(r.meta))
		if err != nil {
			return fmt.Errorf("session %s: %w", r.id, err)
		}
		if !isObject(raw) || len(raw) > recordLimit {
			return fmt.Errorf("%w: session %s record is not a JSON object under %d bytes", ErrRecord, r.id, recordLimit)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE session_records SET meta=?,instructions_hash='',instructions_known_hash='' WHERE id=?", string(raw), r.id); err != nil {
			return err
		}
	}
	for _, column := range []string{"instructions_hash", "instructions_known_hash"} {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE session_records DROP COLUMN "+column); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "DROP TABLE session_instruction_blobs")
	return err
}
