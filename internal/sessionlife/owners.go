package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// The ownership projection: worktree ownership inference needs each record's
// dir, service, room, name and closed state, but not the rest of the record.
// Reading those as typed columns, written with the meta blob in the same
// statement, replaces the legacy read that JSON-decoded every meta row.
// meta_digest detects a meta blob that changed after its columns were
// written, so damaged or externally edited records still fail closed.

// ownerDigestTable is the Castagnoli CRC of a stored meta blob. Integrity,
// not secrecy, is the goal; the checksum runs at line rate on this path.
var ownerDigestTable = crc32.MakeTable(crc32.Castagnoli)

// ownerDigest is the stored digest of the canonical full record written with
// the ownership columns. Instruction blobs participate in that record, so
// changing either the hot row or a referenced blob also breaks the digest.
func ownerDigest(raw []byte) string {
	return strconv.FormatUint(uint64(crc32.Checksum(raw, ownerDigestTable)), 16)
}

// OwnerRow is one session's typed ownership evidence.
type OwnerRow struct {
	ID, Room, Name, Dir string
	Service, Closed     bool
}

// Owners visits every session's ownership row, closed sessions included, in
// name order. It replaces the legacy read that decoded every record's meta
// blob. A row whose meta no longer matches the columns written with it, or
// whose meta names another ID, is an ErrRecord; a caller that removes trees
// must then keep them.
func Owners(sessions string, visit func(OwnerRow) error) error {
	db, ok, err := readable(sessions)
	if err != nil || !ok {
		return err
	}
	res, err := db.Query(`SELECT id, meta_id, room, name, closed, dir, service, meta, meta_digest, ` + instructionReadProjection + ` FROM session_records ORDER BY id`)
	if err != nil {
		return err
	}
	type verified struct {
		owner  OwnerRow
		metaID string
		digest string
	}
	var rows []verified
	for res.Next() {
		var r verified
		var closed int
		// RawBytes stays valid only until the next Next, so the digest is
		// checked before the meta bytes are released.
		var meta sql.RawBytes
		instructions, err := scanInstructionRead(res, &r.owner.ID, &r.metaID, &r.owner.Room, &r.owner.Name, &closed, &r.owner.Dir, &r.owner.Service, &meta, &r.digest)
		if err != nil {
			res.Close()
			return err
		}
		if instructions.hashes != [2]string{} {
			raw, err := instructions.join(meta)
			if err != nil {
				res.Close()
				return fmt.Errorf("%w: session %s: %v", ErrRecord, r.owner.ID, err)
			}
			meta = sql.RawBytes(raw)
		}
		r.owner.Closed = closed != 0
		if r.metaID != "" && r.metaID != r.owner.ID {
			res.Close()
			return fmt.Errorf("%w: session %s record names id %s", ErrRecord, r.owner.ID, r.metaID)
		}
		if r.digest == "" || r.digest != ownerDigest(meta) {
			res.Close()
			return fmt.Errorf("%w: session %s meta does not match its stored columns", ErrRecord, r.owner.ID)
		}
		rows = append(rows, r)
	}
	if err := errors.Join(res.Err(), res.Close()); err != nil {
		return err
	}
	for _, r := range rows {
		if err := visit(r.owner); err != nil {
			return err
		}
	}
	return nil
}

// backfillOwnerColumns adds the ownership columns to an existing store, at
// schema version 4, and imports each row's values from its meta blob once.
// A row whose meta is damaged keeps an empty digest and stays fail-closed
// until the record is rewritten or removed; a row whose meta names another
// ID keeps that ID, which Owners rejects.
func backfillOwnerColumns(ctx context.Context, conn *sql.Conn) error {
	for _, column := range []struct{ name, kind string }{
		{"dir", "TEXT NOT NULL DEFAULT ''"},
		{"service", "INTEGER NOT NULL DEFAULT 0"},
		{"meta_id", "TEXT NOT NULL DEFAULT ''"},
		{"meta_digest", "TEXT NOT NULL DEFAULT ''"},
	} {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE session_records ADD COLUMN "+column.name+" "+column.kind); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT id FROM session_records ORDER BY id")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range ids {
		var meta string
		if err := conn.QueryRowContext(ctx, "SELECT meta FROM session_records WHERE id = ?", id).Scan(&meta); err != nil {
			return err
		}
		raw := []byte(meta)
		var c recordColumns
		var digest string
		if isObject(raw) && len(raw) <= recordLimit && json.Unmarshal(raw, &c) == nil {
			digest = ownerDigest(raw)
		}
		service := 0
		if c.Service {
			service = 1
		}
		if _, err := conn.ExecContext(ctx, "UPDATE session_records SET dir = ?, service = ?, meta_id = ?, meta_digest = ? WHERE id = ?", c.Dir, service, c.ID, digest, id); err != nil {
			return err
		}
	}
	return nil
}
