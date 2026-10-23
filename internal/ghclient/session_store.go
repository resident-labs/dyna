package ghclient

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// sessionAccountSchema remembers, for each room and GitHub CLI configuration,
// the account an access check already proved. A stored choice saves the one
// GitHub API call that a turn would otherwise spend at startup again.
const sessionAccountSchema = `CREATE TABLE IF NOT EXISTS gh_session_accounts (
 key TEXT PRIMARY KEY,
 login TEXT NOT NULL,
 config TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);`

// storedChoiceWrite is how long a stored choice write may wait for the writer
// lock. The write only saves a later probe, so it gives up rather than block a
// turn or a GitHub command.
const storedChoiceWrite = 2 * time.Second

// Migrate creates the stored session account choices in dataDir's dyna.db.
// Only the engine calls it, at start, like every other store (requirement 41).
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return migrateSessionAccounts(db)
}

func migrateSessionAccounts(db *sql.DB) error {
	return database.Migrate(db, "gh_session_accounts", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), sessionAccountSchema)
		return err
	})
}

// sessionStore opens the stored choices. A process that may not migrate finds
// the table the engine created; one that cannot read it has no stored choice.
func sessionStore() (*sql.DB, bool) {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return nil, false
	}
	if err := migrateSessionAccounts(db); err != nil {
		return nil, false
	}
	return db, true
}

// storedChoice returns the account an earlier process proved for key, with the
// expiry it stored. It reports false when the store is unreadable, the row is
// missing, or the GitHub CLI configuration changed since the row was written.
// An expired row is returned too: it is the last known account to use when a
// new probe cannot reach GitHub.
func storedChoice(key, config string) (choice, bool) {
	db, ok := sessionStore()
	if !ok {
		return choice{}, false
	}
	var login, stored string
	var expires int64
	err := db.QueryRow("SELECT login, config, expires_at FROM gh_session_accounts WHERE key=?", key).Scan(&login, &stored, &expires)
	if err != nil || login == "" || stored != config {
		return choice{}, false
	}
	return choice{login: login, until: time.Unix(expires, 0)}, true
}

// rememberChoice stores a proved account for the next turns and for the next
// engine process. A store that cannot be written keeps only the memory choice.
func rememberChoice(key, login, config string, until time.Time) {
	db, ok := sessionStore()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storedChoiceWrite)
	defer cancel()
	_ = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO gh_session_accounts(key,login,config,expires_at) VALUES(?,?,?,?)
			ON CONFLICT(key) DO UPDATE SET login=excluded.login, config=excluded.config, expires_at=excluded.expires_at`,
			key, login, config, until.Unix())
		return err
	})
}

// forgetChoice drops a stored choice the account no longer holds.
func forgetChoice(key string) {
	db, ok := sessionStore()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storedChoiceWrite)
	defer cancel()
	_ = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM gh_session_accounts WHERE key=?", key)
		return err
	})
}

// ghConfigFingerprint names the GitHub CLI configuration a choice was made
// against. A login, logout, or new account rewrites hosts.yml, so a different
// fingerprint sends the next turn back to a real access check. The fingerprint
// holds no secret.
func ghConfigFingerprint(cmd *exec.Cmd) string {
	values := map[string]string{}
	for _, entry := range cmd.Env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	dir := values["GH_CONFIG_DIR"]
	switch {
	case dir != "":
	case values["XDG_CONFIG_HOME"] != "":
		dir = filepath.Join(values["XDG_CONFIG_HOME"], "gh")
	case values["HOME"] != "":
		dir = filepath.Join(values["HOME"], ".config", "gh")
	default:
		return ""
	}
	parts := []string{dir}
	for _, name := range []string{"hosts.yml", "config.yml"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			parts = append(parts, name+":absent")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", name, info.Size(), info.ModTime().UnixNano()))
	}
	return strings.Join(parts, "|")
}
