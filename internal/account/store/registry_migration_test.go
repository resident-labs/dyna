package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	statewrite "dyna-agent/internal/state"
)

func TestRegistryImportPreservesRawMetadataAndCanonicalEmpty(t *testing.T) {
	for _, mode := range []string{"fresh", "canonical empty", "prior mirror", "canonical secret", "credential casing"} {
		t.Run(mode, func(t *testing.T) {
			s := registryStore(t)
			if err := os.MkdirAll(s.Root, 0700); err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"version":1,"entries":[{"id":"legacy","harness":"fixture","identity":"account","label":"Old","needslogin":true,"observation":{"windows":[],"unknown":{"large":9007199254740993}},"unknown":{"large":9007199254740993},"credential":{"schema":"fixture-v1","payload":{"token":"legacy-canary"}}}]}`)
			if mode == "credential casing" {
				raw = bytes.Replace(raw, []byte(`"credential"`), []byte(`"Credential"`), 1)
			}
			if err := os.WriteFile(s.registryPath(), raw, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(filepath.Dir(s.Root))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "canonical empty" || mode == "prior mirror" {
				if err := statewrite.MigrateMirrors(filepath.Dir(s.Root)); err != nil {
					t.Fatal(err)
				}
				if err := database.Migrate(db, "account.registry", 3, func(conn *sql.Conn, _ int) error {
					_, err := conn.ExecContext(context.Background(), registrySchema)
					if err != nil {
						return err
					}
					if mode == "canonical empty" {
						_, err = conn.ExecContext(context.Background(), "INSERT INTO accounts_registry VALUES(1,1,1)")
					} else {
						_, err = conn.ExecContext(context.Background(), "INSERT INTO state_mirrors(key,version,pending) VALUES(?,'','')", registryKey)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "canonical secret" {
				canonical := grant("canonical-canary")
				canonical.Schema = "fixture-v2"
				if err := s.writeCredential("legacy", canonical); err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM accounts").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if mode == "canonical empty" || mode == "prior mirror" {
				if count != 0 {
					t.Fatal("stale JSON resurrected accounts")
				}
			} else {
				var metadata []byte
				if err := db.QueryRow("SELECT metadata FROM accounts WHERE id='legacy'").Scan(&metadata); err != nil {
					t.Fatal("missing eager import:", err)
				}
				if !bytes.Contains(metadata, []byte("9007199254740993")) || bytes.Contains(metadata, []byte("credential")) || bytes.Contains(metadata, []byte("canary")) {
					t.Fatalf("metadata changed or contains credentials: %s", metadata)
				}
				c, ok := s.readCredential("legacy")
				want := "legacy-canary"
				if mode == "canonical secret" {
					want = "canonical-canary"
				}
				if !ok || !bytes.Contains(c.Payload, []byte(want)) {
					t.Fatal("credential was lost or replaced")
				}
				loaded, err := s.Read()
				if err != nil || loaded.Find("legacy") == nil || loaded.Find("legacy").Credential.Schema != c.Schema {
					t.Fatal("canonical credential is not readable through the registry")
				}
				info, err := os.Stat(s.credentialPath("legacy"))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("credential protection changed")
				}
				if err := s.Update(func(st *State) error { st.Find("legacy").Label = "New"; return nil }); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow("SELECT metadata FROM accounts WHERE id='legacy'").Scan(&metadata); err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(metadata, &fields) != nil || string(fields["unknown"]) != `{"large":9007199254740993}` {
					t.Fatal("update lost unknown metadata")
				}
				var observationFields map[string]json.RawMessage
				if json.Unmarshal(fields["observation"], &observationFields) != nil || string(observationFields["unknown"]) != `{"large":9007199254740993}` {
					t.Error("label update lost unknown nested metadata")
				}
				if err := s.Update(func(st *State) error {
					st.Find("legacy").NeedsLogin = false
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				reloaded, err := s.Read()
				if err != nil || reloaded.Find("legacy") == nil || reloaded.Find("legacy").NeedsLogin {
					t.Error("retained field alias restored the cleared login flag")
				}
				if err := s.Update(func(st *State) error { st.Entries = nil; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := os.ReadFile(s.registryPath()); err != nil || !bytes.Equal(got, raw) {
				t.Fatal("retained source changed")
			}
			if err := Migrate(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			st, err := s.Read()
			if err != nil || len(st.Entries) != 0 {
				t.Fatalf("replay resurrected accounts: %v", err)
			}
		})
	}
}
