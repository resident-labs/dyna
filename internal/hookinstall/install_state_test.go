package hookinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestInstallStateImportRefusesInvalidSourceAtomically(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "hooks", "install")}
	db, err := database.Open(s.dataDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(s.dataDir(), "one"), filepath.Join(s.dataDir(), "two")}
	err = database.Migrate(db, "hooks.install", 1, func(c *sql.Conn, _ int) error {
		if _, err := c.ExecContext(context.Background(), jobsSchema); err != nil {
			return err
		}
		for i, p := range paths {
			body, _ := json.Marshal(Job{Config: Config{State: p}})
			if _, err := c.ExecContext(context.Background(), "INSERT INTO hook_install_jobs(id,body) VALUES(?,?)", strings.Repeat(string(rune('a'+i)), 32), body); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		value := strings.Repeat("a", 40)
		if i == 1 {
			value = "invalid"
		}
		if err := os.WriteFile(p, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db(); err == nil {
		t.Fatal("invalid source accepted")
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='hooks.install'").Scan(&version); err != nil || version != "1" {
		t.Fatalf("version %s: %v", version, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='hook_install_state'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial state: %d %v", count, err)
	}
	if err := os.WriteFile(paths[1], []byte(strings.Repeat("b", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], []byte(strings.Repeat("c", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db(); err != nil {
		t.Fatal(err)
	}
	var installed string
	err = db.QueryRow("SELECT installed_commit FROM hook_install_state WHERE state=?", paths[0]).Scan(&installed)
	if err != nil || installed != strings.Repeat("a", 40) {
		t.Fatalf("state %s: %v", installed, err)
	}
}
