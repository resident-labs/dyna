package sessionlife

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// session_steering holds the steering messages of each session's finished
// turns, copied once from turns/<n>.json, so a receipt check is one query
// instead of decoding every turn file. A finished turn that is not the
// session's latest never gains steering or receipts. session_steering_through
// is the last turn copied; every turn up to it is in session_steering.
const steeringSchema = `CREATE TABLE IF NOT EXISTS session_steering (
  session_id TEXT NOT NULL,
  turn INTEGER NOT NULL,
  message_id TEXT NOT NULL,
  delivered INTEGER NOT NULL,
  PRIMARY KEY (session_id, turn, message_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_steering_through (
  session_id TEXT PRIMARY KEY,
  turn INTEGER NOT NULL
) WITHOUT ROWID;`

// Steer is one steering message of a turn, by its room post ID; messages
// with no post ID are left out.
type Steer struct {
	Turn      int
	ID        string
	Delivered bool
}

// TurnSteering reads turn n's steering from its file, and whether the turn
// has finished.
type TurnSteering func(n int) (steers []Steer, finished bool, err error)

func steeringDB(sessions string) (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "sessions.steering", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), steeringSchema)
		return err
	})
	return db, err
}

// Steering returns the steering of session id's turns 1 to turns, in turn
// order. Turns already copied come from one query. The others are read
// through read; each one that has finished and is not the latest is copied,
// so later calls read only the latest turns.
func Steering(sessions, id string, turns int, read TurnSteering) ([]Steer, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	db, err := steeringDB(sessions)
	if err != nil {
		return nil, err
	}
	var through int
	err = db.QueryRow("SELECT turn FROM session_steering_through WHERE session_id = ?", id).Scan(&through)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	through = min(through, turns)
	out, err := copiedSteering(db, id, through)
	if err != nil {
		return nil, err
	}
	var copied, live []Steer
	next := through
	for n := through + 1; n <= turns; n++ {
		steers, finished, err := read(n)
		if err != nil {
			return nil, err
		}
		if finished && n < turns && next == n-1 {
			copied, next = append(copied, steers...), n
		} else {
			live = append(live, steers...)
		}
	}
	if next > through {
		if err := copySteering(db, id, next, copied); err != nil {
			return nil, err
		}
	}
	out = append(append(out, copied...), live...)
	return out, nil
}

func copiedSteering(db *sql.DB, id string, through int) ([]Steer, error) {
	if through == 0 {
		return nil, nil
	}
	rows, err := db.Query("SELECT turn, message_id, delivered FROM session_steering WHERE session_id = ? AND turn <= ? ORDER BY turn", id, through)
	if err != nil {
		return nil, err
	}
	var out []Steer
	for rows.Next() {
		var st Steer
		if err := rows.Scan(&st.Turn, &st.ID, &st.Delivered); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, st)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// copySteering stores finished turns' steering and moves through forward.
// Copies by two readers at once agree, so the later one changes nothing.
func copySteering(db *sql.DB, id string, through int, steers []Steer) error {
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		for _, st := range steers {
			_, err := conn.ExecContext(ctx, `INSERT INTO session_steering(session_id, turn, message_id, delivered) VALUES (?, ?, ?, ?)
ON CONFLICT DO UPDATE SET delivered = MAX(delivered, excluded.delivered)`, id, st.Turn, st.ID, st.Delivered)
			if err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO session_steering_through(session_id, turn) VALUES (?, ?)
ON CONFLICT DO UPDATE SET turn = MAX(turn, excluded.turn)`, id, through)
		return err
	})
}

// deleteSteering removes session id's copied steering, inside Delete's
// transaction; Delete prepares the tables first.
func deleteSteering(conn *sql.Conn, id string) error {
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, "DELETE FROM session_steering WHERE session_id = ?", id); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "DELETE FROM session_steering_through WHERE session_id = ?", id)
	return err
}
