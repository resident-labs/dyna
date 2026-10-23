package room

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
)

const postNormalizationsSchema = `CREATE TABLE room_post_normalizations (
 room_id TEXT NOT NULL,
 source_start INTEGER NOT NULL CHECK(source_start>=0),
 source_end INTEGER NOT NULL CHECK(source_end>source_start),
 digest BLOB NOT NULL,
 body BLOB NOT NULL CHECK(json_valid(body) AND json_type(body)='object'),
 omitted INTEGER NOT NULL DEFAULT 0 CHECK(omitted IN (0,1)),
 PRIMARY KEY(room_id, source_start)
);`

const postNormalizationsRevision = "room.post_normalizations.revision"

type postNormalization struct {
	end     int64
	digest  []byte
	body    []byte
	omitted bool
}

func (s *Store) postNormalizationsDB() (*sql.DB, error) {
	db, err := s.recordsDB()
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(db, "room.post_normalizations", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), postNormalizationsSchema)
		return err
	}); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.posts.wake", 1, func(conn *sql.Conn, _ int) error {
		return s.convertHistoricalPosts(context.Background(), conn, func(source, current map[string]json.RawMessage) (bool, error) {
			if wake, explicit := source["wake"]; explicit && !bytes.Equal(wake, []byte("null")) {
				return false, nil
			}
			var kind string
			if err := json.Unmarshal(source["kind"], &kind); err != nil && source["kind"] != nil {
				// The message reader skips invalid records; migration must do the same.
				return false, nil
			}
			current["wake"] = json.RawMessage("true")
			if kind == "instructions" {
				current["wake"] = json.RawMessage("false")
			}
			return true, nil
		})
	})
	if err != nil {
		return nil, err
	}
	for _, migration := range []struct {
		name    string
		convert func(map[string]json.RawMessage, map[string]json.RawMessage) (bool, error)
		omit    func(map[string]json.RawMessage) bool
	}{
		{"room.posts.card_versions", historicalCardVersions, nil},
		{"room.posts.shell_output", historicalShellOutput, nil},
		{"room.posts.questions", historicalQuestionKinds, historicalDuplicateAnswer},
	} {
		if err := database.Migrate(db, migration.name, 1, func(conn *sql.Conn, _ int) error {
			return s.convertHistoricalPosts(context.Background(), conn, migration.convert, migration.omit)
		}); err != nil {
			return nil, err
		}
	}
	return db, nil
}

// TODO: legacy/shim remove later — retained first HTML cards omitted version 1.
func historicalCardVersions(source, current map[string]json.RawMessage) (bool, error) {
	var attachments []map[string]json.RawMessage
	if json.Unmarshal(current["attachments"], &attachments) != nil {
		return false, nil
	}
	changed := false
	for _, attachment := range attachments {
		var render, visual string
		var version int
		if json.Unmarshal(attachment["render"], &render) != nil || render != "html" {
			continue
		}
		if raw := attachment["visual"]; raw != nil && json.Unmarshal(raw, &visual) != nil {
			continue
		}
		if raw := attachment["version"]; raw != nil && json.Unmarshal(raw, &version) != nil {
			continue
		}
		if visual == "" && version == 0 {
			attachment["version"] = json.RawMessage("1")
			changed = true
		}
	}
	if changed {
		raw, err := json.Marshal(attachments)
		if err != nil {
			return false, err
		}
		current["attachments"] = raw
	}
	return changed, nil
}

// TODO: legacy/shim remove later — retained shell exits used an inline output pointer.
func historicalShellOutput(source, current map[string]json.RawMessage) (bool, error) {
	var from, text string
	if json.Unmarshal(source["from"], &from) != nil || !strings.HasPrefix(from, "shell:") || from == "shell:" {
		return false, nil
	}
	if json.Unmarshal(current["text"], &text) != nil {
		return false, nil
	}
	suffix := " Output: dyna shell tail " + strings.TrimPrefix(from, "shell:")
	if !strings.HasSuffix(text, suffix) {
		return false, nil
	}
	raw, err := json.Marshal(strings.TrimSuffix(text, suffix) + "\n" + strings.TrimPrefix(suffix, " "))
	if err != nil {
		return false, err
	}
	current["text"] = raw
	return true, nil
}

// TODO: legacy/shim remove later — retained questions used their own kind, and answers repeated as events.
// The omit hook marks a duplicate answer event in the omitted column.
func historicalQuestionKinds(source, current map[string]json.RawMessage) (bool, error) {
	var kind string
	if json.Unmarshal(source["kind"], &kind) != nil {
		return false, nil
	}
	switch kind {
	case "question":
		current["kind"] = json.RawMessage(`"message"`)
		return true, nil
	}
	return false, nil
}

func historicalDuplicateAnswer(source map[string]json.RawMessage) bool {
	var kind string
	return json.Unmarshal(source["kind"], &kind) == nil && kind == "question-answered"
}

// TODO: legacy/shim remove later — remove after retained logs have explicit wake intent.
// convertHistoricalPosts runs only inside a versioned startup migration.
// The original supplies that shape's old input. The current projection supplies
// output state, so each conversion preserves previous changes and unknown fields.
// A conversion never changes source bytes, cursor offsets or omitted state.
func (s *Store) convertHistoricalPosts(ctx context.Context, conn *sql.Conn, convert func(source, current map[string]json.RawMessage) (bool, error), omit ...func(map[string]json.RawMessage) bool) error {
	entries, err := os.ReadDir(s.root())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	changed := false
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		path := filepath.Join(s.root(), entry.Name(), "messages.jsonl")
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		updated, err := convertPostLines(ctx, conn, entry.Name(), f, convert, omit...)
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("convert room %s posts: %w", entry.Name(), err)
		}
		if closeErr != nil {
			return closeErr
		}
		changed = changed || updated
	}
	if changed {
		_, err = conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES (?,'1')
ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`, postNormalizationsRevision)
	}
	return err
}

func convertPostLines(ctx context.Context, conn *sql.Conn, roomID string, input io.Reader, convert func(source, current map[string]json.RawMessage) (bool, error), omit ...func(map[string]json.RawMessage) bool) (bool, error) {
	r := bufio.NewReaderSize(input, 64*1024)
	var offset int64
	changed := false
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			if err != nil && err != io.EOF {
				return false, err
			}
			return changed, nil
		}
		start := offset
		offset += int64(len(line))
		// The shared reader also skips malformed and oversized source records.
		var source map[string]json.RawMessage
		if len(line) > maxRecordBytes || json.Unmarshal(line, &source) != nil || source == nil {
			continue
		}
		var old postNormalization
		err = conn.QueryRowContext(ctx, `SELECT source_end,digest,body,omitted FROM room_post_normalizations
WHERE room_id=? AND source_start=?`, roomID, start).Scan(&old.end, &old.digest, &old.body, &old.omitted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		digest := sha256.Sum256(line)
		current := make(map[string]json.RawMessage, len(source))
		if err == nil {
			if old.end != offset || !bytes.Equal(old.digest, digest[:]) {
				return false, fmt.Errorf("historical post source changed at offset %d", start)
			}
			if err := json.Unmarshal(old.body, &current); err != nil {
				return false, err
			}
		} else {
			for key, value := range source {
				current[key] = value
			}
		}
		updated, err := convert(source, current)
		if err != nil {
			return false, err
		}
		hide := len(omit) > 0 && omit[0] != nil && omit[0](source)
		if !updated && !hide {
			continue
		}
		body, err := json.Marshal(current)
		if err != nil {
			return false, err
		}
		// Updating omitted only on request preserves an earlier migration's omitted state.
		_, err = conn.ExecContext(ctx, `INSERT INTO room_post_normalizations(room_id,source_start,source_end,digest,body,omitted)
VALUES (?,?,?,?,?,?) ON CONFLICT(room_id,source_start) DO UPDATE SET body=excluded.body, omitted=omitted OR excluded.omitted`, roomID, start, offset, digest[:], body, hide)
		if err != nil {
			return false, err
		}
		changed = true
	}
}

func (s *Store) postNormalizationRevision() (int64, error) {
	db, err := s.postNormalizationsDB()
	if err != nil {
		return 0, err
	}
	var revision int64
	err = db.QueryRow("SELECT COALESCE((SELECT value FROM meta WHERE key=?),'0')", postNormalizationsRevision).Scan(&revision)
	return revision, err
}

func (s *Store) postNormalizations(roomID string, offset int64) (map[int64]postNormalization, error) {
	db, err := s.postNormalizationsDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT source_start,source_end,digest,body,omitted
FROM room_post_normalizations WHERE room_id=? AND source_start>=?`, roomID, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]postNormalization{}
	for rows.Next() {
		var start int64
		var p postNormalization
		if err := rows.Scan(&start, &p.end, &p.digest, &p.body, &p.omitted); err != nil {
			return nil, err
		}
		out[start] = p
	}
	return out, rows.Err()
}
