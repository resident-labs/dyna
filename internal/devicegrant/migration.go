package devicegrant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/scope"
)

// TODO: legacy/shim remove later — reads retained token files during startup migration.
// importDeviceScopes runs once inside the grant schema migration. The former
// import checkpoint prevents a later token file from gaining an implicit grant.
// Existing explicit scopes always win; source token records stay unchanged.
func importDeviceScopes(conn *sql.Conn, dataDir string) error {
	ctx := context.Background()
	var checkpoint string
	err := conn.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", "device_grants.legacy_imported").Scan(&checkpoint)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	scopes, err := json.Marshal(scope.All())
	if err != nil {
		return err
	}
	for _, source := range []struct{ kind, path string }{
		{"paired", filepath.Join(dataDir, "web", "devices.json")},
		{"connect", filepath.Join(dataDir, "connect", "sessions.json")},
	} {
		raw, err := os.ReadFile(source.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var records []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &records); err != nil {
			return fmt.Errorf("import %s device scopes: %w", source.kind, err)
		}
		for _, record := range records {
			if record.ID == "" {
				continue
			}
			if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO device_grants(kind,id,scopes,legacy) VALUES(?,?,?,1)", source.kind, record.ID, string(scopes)); err != nil {
				return err
			}
		}
	}
	return nil
}
