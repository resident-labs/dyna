package userservices

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestInstallRetiresLegacyEngineRestart(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "retire", true: "stop failure"}[stopFails], func(t *testing.T) {
			db, err := database.Open(storetest.DataDir(t))
			if err != nil {
				t.Fatal(err)
			}
			dir := environment(t)
			unit := filepath.Join(dir, "dyna-engine-restart.service")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unit, []byte("legacy helper"), 0644); err != nil {
				t.Fatal(err)
			}
			key := "userservices.engine-restart:" + filepath.Clean(dir)
			if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(t.Context(), "INSERT INTO meta(key,value) VALUES(?,?)", key, "123")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			stopped, restarted := false, false
			err = Install(t.Context(), Options{Binary: "/test/dyna", Dir: dir, Run: func(_ context.Context, args ...string) (string, error) {
				command := strings.Join(args, " ")
				if command == "systemctl --user stop dyna-engine-restart.service" {
					if stopFails {
						return "", errors.New("stop failed")
					}
					stopped = true
				}
				if command == "systemctl --user restart "+Engine {
					restarted = true
					if !stopped {
						t.Fatal("engine restarted before legacy helper stopped")
					}
				}
				return "Transient=no\nLoadState=loaded\n", nil
			}})
			if (err != nil) != stopFails || restarted == stopFails {
				t.Fatalf("err=%v restarted=%v stopFails=%v", err, restarted, stopFails)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM meta WHERE key=?", key).Scan(&count); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(unit)
			if stopFails && (count != 1 || statErr != nil) {
				t.Fatal("failed stop discarded retry state")
			}
			if !stopFails && (count != 0 || !os.IsNotExist(statErr)) {
				t.Fatalf("legacy state remains: count=%d file error=%v", count, statErr)
			}
		})
	}
}
