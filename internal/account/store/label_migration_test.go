package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestPiLabelMigration(t *testing.T) {
	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const generated = "00112233445566778899aabbccddeeff"
	const custom = "ffeeddccbbaa99887766554433221100"
	if err := database.Migrate(db, "account.registry", 10, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(ctx, registrySchema); err != nil {
			return err
		}
		for _, row := range []Metadata{
			{ID: generated, Harness: "pi", Identity: "pi:openai-codex", Label: "pi native login"},
			{ID: custom, Harness: "pi", Identity: "pi:openai-codex", Label: "My account"},
		} {
			raw, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if _, err = conn.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES (?, 0, ?)", row.ID, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{generated, custom} {
		dir := filepath.Join(dataDir, "accounts", "pi", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai-codex":{"type":"api_key","key":"secret1234"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, label string
		source    LabelSource
	}{
		{generated, "openai-codex · 1234", LabelDefault},
		{custom, "My account", LabelOwner},
	} {
		var raw []byte
		if err := db.QueryRowContext(ctx, "SELECT metadata FROM accounts WHERE id = ?", tc.id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var got Metadata
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Label != tc.label || got.LabelSource != tc.source {
			t.Errorf("label = %q (%s), want %q (%s)", got.Label, got.LabelSource, tc.label, tc.source)
		}
	}
	// A second open does not revisit a migrated default.
	if _, err := db.ExecContext(ctx, `UPDATE accounts SET metadata=json_set(metadata,'$.label','pi native login') WHERE id=?`, generated); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var label string
	if err := db.QueryRowContext(ctx, `SELECT json_extract(metadata,'$.label') FROM accounts WHERE id=?`, generated).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "pi native login" {
		t.Fatalf("migration ran twice: %q", label)
	}
}
