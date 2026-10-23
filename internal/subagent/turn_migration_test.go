package subagent

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestPostMigrationTurnUpdateKeepsClearedCanonicalFields(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	path := turnFile(t, s, id, 1)
	stale := []byte(`{"n":1,"status":"running","prompt":"stale prompt","error":"stale error","steering":[{"id":"old-post"}],"deliveries":[{"outcome":"delivered"}]}`)
	writeStale := func() {
		t.Helper()
		if err := os.WriteFile(path+".stale", stale, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".stale", path); err != nil {
			t.Fatal(err)
		}
	}
	writeStale()
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	check := func(turn Turn, profile string) {
		t.Helper()
		if turn.Prompt != "" || turn.Status != TurnOK || turn.Error != "" || len(turn.Steering) != 0 || len(turn.Deliveries) != 0 || turn.Profile != profile {
			t.Errorf("update resurrected cleared canonical fields: %+v", turn)
		}
	}
	checkCopy := func(profile string) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var copied Turn
		if err := json.Unmarshal(raw, &copied); err != nil {
			t.Fatal(err)
		}
		check(copied, profile)
	}
	got, err := s.UpdateTurn(id, 1, func(turn *Turn) { turn.Profile = "current-profile" })
	if err != nil {
		t.Fatal(err)
	}
	check(got, "current-profile")
	checkCopy("current-profile")
	writeStale()
	before, ok := s.turnVersion(id, 1)
	if !ok {
		t.Fatal("canonical turn has no revision")
	}
	if err := s.ImportLegacyTurn(id, 1); err != nil {
		t.Fatal(err)
	}
	if after, ok := s.turnVersion(id, 1); !ok || after != before {
		t.Errorf("watcher changed canonical revision: %s -> %s", before, after)
	}
	got, err = s.ReadTurn(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	check(got, "current-profile")
	got, err = s.UpdateTurn(id, 1, func(turn *Turn) { turn.Profile = "next-profile" })
	if err != nil {
		t.Fatal(err)
	}
	check(got, "next-profile")
	checkCopy("next-profile")
	got, err = s.ReadTurn(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	check(got, "next-profile")
	// A direct canonical write must not read an unusable retained copy.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnOK, Profile: "current-profile"}); err != nil {
		t.Fatalf("canonical write read the retained copy: %v", err)
	}
}

func TestMigrateImportsRetainedTurnBeforeAnyRead(t *testing.T) {
	for _, version := range []string{"", "1"} {
		t.Run("prior_version_"+version, func(t *testing.T) {
			s, id := turnStore(t)
			db, err := s.turnsDB()
			if err != nil {
				t.Fatal(err)
			}
			if version != "" {
				if _, err := db.Exec("INSERT INTO meta(key,value) VALUES('subagent.turns.import',?)", version); err != nil {
					t.Fatal(err)
				}
			}
			// A stale malformed copy must never defeat its canonical row.
			if _, err := db.Exec(`INSERT INTO session_turns(session,n,revision,turn) VALUES(?,2,1,'{"n":2,"prompt":"canonical"}')`, id); err != nil {
				t.Fatal(err)
			}
			raw := []byte("{\n  \"n\": 1, \"prompt\": \"retained\", \"future\": {\"keep\": true}, \"usageNormalization\": {\"version\": 1}\n}\n")
			path := turnFile(t, s, id, 1)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(turnFile(t, s, id, 2), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			// No session record or turn count exists for this artifact folder.
			if err := Migrate(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			var got []byte
			if err := db.QueryRow("SELECT turn FROM session_turns WHERE session=? AND n=1", id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			resolved, err := resolveRowPrompts(t.Context(), db, id, 1, got)
			if err != nil {
				t.Fatal(err)
			}
			var original, imported any
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(resolved, &imported); err != nil || !reflect.DeepEqual(original, imported) {
				t.Fatalf("startup import = %s, %v; want original content before any turn read", resolved, err)
			}
			if preserved, err := os.ReadFile(path); err != nil || !bytes.Equal(preserved, raw) {
				t.Fatalf("source changed: %s, %v", preserved, err)
			}
			backups, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Root), "backups", database.PreMigrationFolder, "dyna-*.db"))
			if len(backups) != 1 {
				t.Fatalf("backups = %v; want one before import", backups)
			}
			backup, err := sql.Open("sqlite", backups[0])
			if err != nil {
				t.Fatal(err)
			}
			defer backup.Close()
			var count int
			if err := backup.QueryRow("SELECT count(*) FROM session_turns WHERE n=1").Scan(&count); err != nil || count != 0 {
				t.Fatalf("backup already contains imported turn: %d, %v", count, err)
			}
			var prior string
			err = backup.QueryRow("SELECT value FROM meta WHERE key='subagent.turns.import'").Scan(&prior)
			if version == "" && err != sql.ErrNoRows || version != "" && (err != nil || prior != version) {
				t.Fatalf("backup import version = %q, %v; want prior version %q", prior, err, version)
			}
			if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(filepath.Dir(s.Root)); err != nil {
				t.Fatalf("completed import replayed retained files: %v", err)
			}
		})
	}
	for _, badKind := range []string{"number", "json", "symlink"} {
		t.Run("rollback_"+badKind, func(t *testing.T) {
			s, id := turnStore(t)
			if err := os.WriteFile(turnFile(t, s, id, 1), []byte(`{"n":1}`), 0600); err != nil {
				t.Fatal(err)
			}
			bad := turnFile(t, s, id, 2)
			switch badKind {
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(outside, []byte(`{"n":2}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, bad); err != nil {
					t.Fatal(err)
				}
			default:
				raw := []byte(`{"n":3}`)
				if badKind == "json" {
					raw = []byte("{")
				}
				if err := os.WriteFile(bad, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(filepath.Dir(s.Root)); err == nil || !strings.Contains(err.Error(), bad) {
				t.Fatalf("malformed retained turn error = %v; want its source path", err)
			}
			db, err := database.Open(filepath.Dir(s.Root))
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM session_turns").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial import survived rollback: %d, %v", count, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM meta WHERE key='subagent.turns.import'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed import stamped completion: %d, %v", count, err)
			}
		})
	}
}
