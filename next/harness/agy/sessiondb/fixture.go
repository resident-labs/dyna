package sessiondb

import (
	"database/sql"
	"os"
	"path/filepath"
)

// AppendStep adds a step to a session log, creating the database with T3's reader schema (steps: idx, step_type,
// step_payload) when there is none. It is for tests: the fake agent in fakeagent writes the error step a real agy writes.
// Production code never writes.
func AppendStep(profileDir, sessionID string, stepType int, payload string) error {
	path := filepath.Join(profileDir, filepath.Join(conversations...), sessionID+".db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(2000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS steps (idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL, step_payload BLOB)"); err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO steps (idx, step_type, step_payload) VALUES ((SELECT COALESCE(max(idx), -1) + 1 FROM steps), ?, ?)", stepType, []byte(payload))
	return err
}

// Step types the fake agent and tests write.
const (
	StepError           = errorStepType
	StepPlannerResponse = plannerResponseStepType
)
