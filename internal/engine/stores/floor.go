package stores

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
)

// floorRequirement names an eagerly completed import and the last release
// that can bring a store through it. The empty floor accepts all old stores.
type floorRequirement struct {
	key     string
	release string
}

var upgradeFloor = []floorRequirement{}

// legacyStateFiles are paths used by stores before SQLite. Patterns are
// relative to the data directory and exclude settings and other configuration.
var legacyStateFiles = []string{
	"goals/state.json", "notices.json", "update-check.json",
	"worktrees/*.json", "computers/*/meta.json", "computers/*/state.json", "computers/*/failure.json",
	"rooms/*/room.json", "rooms/*/seat.json", "rooms/*/pins.json", "rooms/*/issues.json", "rooms/*/owner-read.json",
	"sessions/*/session.json", "sessions/*/queue.json", "sessions/*/turns/*.json",
}

// UpgradeFloorError says which release can import an unsupported store.
type UpgradeFloorError struct{ Release string }

func (e *UpgradeFloorError) Error() string {
	return fmt.Sprintf("This store is from an older release. Install release %s, start it once, then install this release.", e.Release)
}

// CheckFloor refuses unsupported stores before any startup migration.
func CheckFloor(dir string) error {
	newStore, err := checkFloor(dir, upgradeFloor)
	if err != nil {
		return err
	}
	if newStore {
		return markNewFloor(dir, upgradeFloor)
	}
	return nil
}

// checkFloor uses a read-only SQLite connection before any migration or schema
// initialization. A missing database with legacy files is not a new store.
func checkFloor(dir string, floor []floorRequirement) (bool, error) {
	if len(floor) == 0 {
		return false, nil
	}
	path := filepath.Join(dir, "dyna.db")
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		for _, pattern := range legacyStateFiles {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				return false, err
			}
			if len(matches) > 0 {
				return false, &UpgradeFloorError{Release: floor[0].release}
			}
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sql.Open("dyna-sqlite", u.String())
	if err != nil {
		return false, err
	}
	defer db.Close()
	var meta bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='meta')").Scan(&meta); err != nil {
		return false, err
	}
	for _, requirement := range floor {
		if !meta {
			return false, &UpgradeFloorError{Release: requirement.release}
		}
		var found bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM meta WHERE key=?)", requirement.key).Scan(&found); err != nil {
			return false, err
		}
		if !found {
			return false, &UpgradeFloorError{Release: requirement.release}
		}
	}
	return false, nil
}

// markNewFloor records requirements only for a truly new store.
func markNewFloor(dir string, floor []floorRequirement) error {
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		for _, requirement := range floor {
			if _, err := conn.ExecContext(context.Background(), "INSERT OR IGNORE INTO meta(key,value) VALUES (?, '1')", requirement.key); err != nil {
				return err
			}
		}
		return nil
	})
}
