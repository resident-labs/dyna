package approvals

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const schema = `CREATE TABLE approval_requests (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 room TEXT NOT NULL,
 id TEXT NOT NULL,
 requester TEXT NOT NULL,
 kind TEXT NOT NULL,
 class TEXT NOT NULL,
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 payload_hash TEXT NOT NULL,
 state TEXT NOT NULL,
 at TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 result TEXT CHECK(result IS NULL OR json_valid(result)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(room,id)
);
CREATE UNIQUE INDEX approval_request_duplicate ON approval_requests(room,requester,payload_hash) WHERE state='pending';
CREATE INDEX approval_request_holder ON approval_requests(at,state);
CREATE INDEX approval_request_expiry ON approval_requests(state,expires_at);
CREATE TABLE approval_decisions (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 request TEXT NOT NULL,
 actor TEXT NOT NULL,
 action TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 at INTEGER NOT NULL
);
CREATE INDEX approval_decision_request ON approval_decisions(request,seq);
CREATE TABLE approval_rules (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 room TEXT NOT NULL,
 kind TEXT NOT NULL,
 command TEXT NOT NULL DEFAULT '',
 server TEXT NOT NULL DEFAULT '',
 tool TEXT NOT NULL DEFAULT '',
 decide TEXT NOT NULL,
 created_at INTEGER NOT NULL
);`

var idPattern = regexp.MustCompile(`^A-[1-9][0-9]*$`)

// Migrate creates the approval tables. Only engine startup calls it.
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "approvals.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
}

func (s *Service) db() (*sql.DB, error) {
	dataDir := s.DataDir
	if dataDir == "" {
		dataDir = filepath.Dir(s.Rooms.Root)
	}
	return database.Open(dataDir)
}

type rowScanner interface {
	Scan(dest ...any) error
}

const requestColumns = `room,id,requester,kind,class,payload,payload_hash,state,at,expires_at,result,created_at,updated_at`

func scanRequest(row rowScanner) (Request, error) {
	var r Request
	var payload, result string
	var expires, created, updated int64
	var nullableResult sql.NullString
	err := row.Scan(&r.Room, &r.ID, &r.Requester, &r.Kind, &r.Class, &payload, &r.PayloadHash, &r.State, &r.atID, &expires, &nullableResult, &created, &updated)
	if err != nil {
		return Request{}, err
	}
	r.Payload = json.RawMessage(payload)
	r.At = r.atID
	r.ExpiresAt = time.Unix(0, expires).UTC()
	r.CreatedAt = time.Unix(0, created).UTC()
	r.UpdatedAt = time.Unix(0, updated).UTC()
	if nullableResult.Valid {
		result = nullableResult.String
		var outcome Result
		if err := json.Unmarshal([]byte(result), &outcome); err != nil {
			return Request{}, fmt.Errorf("decode approval %s result: %w", r.ID, err)
		}
		r.Result = &outcome
	}
	return r, nil
}

func (s *Service) get(roomID, id string) (Request, error) {
	if !idPattern.MatchString(id) {
		return Request{}, ErrNotFound
	}
	db, err := s.db()
	if err != nil {
		return Request{}, err
	}
	r, err := scanRequest(db.QueryRow(`SELECT `+requestColumns+` FROM approval_requests WHERE room=? AND id=?`, roomID, id))
	if err == sql.ErrNoRows {
		return Request{}, ErrNotFound
	}
	return r, err
}

func (s *Service) duplicate(roomID, requester, hash string) (Request, error) {
	db, err := s.db()
	if err != nil {
		return Request{}, err
	}
	r, err := scanRequest(db.QueryRow(`SELECT `+requestColumns+` FROM approval_requests WHERE room=? AND requester=? AND payload_hash=? AND state='pending'`, roomID, requester, hash))
	if err == sql.ErrNoRows {
		return Request{}, nil
	}
	return r, err
}

func (s *Service) pending(roomID string) ([]Request, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT `+requestColumns+` FROM approval_requests WHERE room=? AND state='pending' ORDER BY seq`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readRequests(rows)
}

func (s *Service) due(now time.Time) ([]Request, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT `+requestColumns+` FROM approval_requests WHERE state='pending' AND expires_at<=? ORDER BY seq`, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readRequests(rows)
}

func readRequests(rows *sql.Rows) ([]Request, error) {
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func insertRequest(ctx context.Context, conn *sql.Conn, r *Request) error {
	var number int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(substr(id,3) AS INTEGER)),0)+1 FROM approval_requests WHERE room=?`, r.Room).Scan(&number); err != nil {
		return err
	}
	r.ID = fmt.Sprintf("A-%d", number)
	_, err := conn.ExecContext(ctx, `INSERT INTO approval_requests(room,id,requester,kind,class,payload,payload_hash,state,at,expires_at,result,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
		r.Room, r.ID, r.Requester, r.Kind, r.Class, []byte(r.Payload), r.PayloadHash, r.State, r.atID, r.ExpiresAt.UnixNano(), r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano())
	return err
}

func insertDecision(ctx context.Context, conn *sql.Conn, request, actor string, action Action, reason string, at time.Time) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO approval_decisions(request,actor,action,reason,at) VALUES(?,?,?,?,?)`, request, actor, action, reason, at.UnixNano())
	return err
}

func writeResult(r Request) ([]byte, error) {
	if r.Result == nil {
		return nil, nil
	}
	return json.Marshal(r.Result)
}

func (s *Service) write(ctx context.Context, fn func(*sql.Conn) error) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, fn)
}
