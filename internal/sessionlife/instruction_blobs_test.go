package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/database"
)

func TestInstructionBlobMigrationDedupsAndPreservesRecords(t *testing.T) {
	a := `{ "id" : "a", "dir" : "/trees/a", "instructions" : {"text":"shared","nested":{"keep":true}}, "instructionsKnown" : {"text":"known"}, "future" : {"keep":[1,"two"]} }`
	b := `{"id":"b","dir":"/trees/b","instructions":{"text":"shared","nested":{"keep":true}},"instructionsKnown":{"text":"known"},"future":{"other":"value"}}`
	sessions := v3Store(t, [2]string{"a", a}, [2]string{"b", b})
	if _, err := records(sessions); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"a": a, "b": b} {
		raw, _, err := LoadRaw(sessions, id)
		if err != nil || string(raw) != want {
			t.Fatalf("session %s bytes changed during migration: %q, %v", id, raw, err)
		}
		var got, expected map[string]json.RawMessage
		if err := Load(sessions, id, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("session %s record changed during migration", id)
		}
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	var blobs int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_instruction_blobs").Scan(&blobs); err != nil || blobs != 2 {
		t.Fatalf("shared instruction blobs = %d, %v; want 2", blobs, err)
	}
	var hashA, hashB string
	if err := db.QueryRow("SELECT instructions_hash FROM session_records WHERE id='a'").Scan(&hashA); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT instructions_hash FROM session_records WHERE id='b'").Scan(&hashB); err != nil {
		t.Fatal(err)
	}
	if hashA == "" || hashA != hashB {
		t.Fatal("equal instruction texts did not share their content hash")
	}
	var revision int
	if err := db.QueryRow("SELECT revision FROM session_records WHERE id='a'").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("migration changed revision to %d, %v", revision, err)
	}
	before, _ := RecordVersion(sessions, "a")
	var fields map[string]json.RawMessage
	if err := Load(sessions, "a", &fields); err != nil {
		t.Fatal(err)
	}
	fields["usage"] = json.RawMessage(`{"input":1}`)
	if err := Save(sessions, "a", fields); err != nil {
		t.Fatal(err)
	}
	if after, _ := RecordVersion(sessions, "a"); after == before {
		t.Fatal("hot update did not advance the record version")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM session_instruction_blobs").Scan(&blobs); err != nil || blobs != 2 {
		t.Fatalf("hot update changed cold blobs = %d, %v", blobs, err)
	}
	fields["instructions"] = json.RawMessage(`{"text":"changed","nested":{"keep":true}}`)
	if err := Save(sessions, "a", fields); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM session_instruction_blobs").Scan(&blobs); err != nil || blobs != 3 {
		t.Fatalf("cold update blobs = %d, %v; want one new value", blobs, err)
	}
	before, _ = RecordVersion(sessions, "a")
	fields["instructionsKnown"] = json.RawMessage(`{"text":"transactional"}`)
	if err := database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		return SaveTx(conn, "a", fields)
	}); err != nil {
		t.Fatal(err)
	}
	if after, _ := RecordVersion(sessions, "a"); after == before {
		t.Fatal("transactional cold update did not advance the record version")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM session_instruction_blobs").Scan(&blobs); err != nil || blobs != 4 {
		t.Fatalf("transactional cold update blobs = %d, %v", blobs, err)
	}
	var gotA, gotB map[string]json.RawMessage
	if err := Load(sessions, "a", &gotA); err != nil {
		t.Fatal(err)
	}
	if err := Load(sessions, "b", &gotB); err != nil {
		t.Fatal(err)
	}
	if string(gotA["instructions"]) != `{"text":"changed","nested":{"keep":true}}` || string(gotB["instructions"]) == string(gotA["instructions"]) {
		t.Fatal("cold update did not remain isolated to its session")
	}
	if string(gotA["instructionsKnown"]) != `{"text":"transactional"}` {
		t.Fatalf("transactional cold update was not read back: %s", gotA["instructionsKnown"])
	}
	if string(gotA["future"]) != `{"keep":[1,"two"]}` {
		t.Fatalf("unknown field changed: %s", gotA["future"])
	}
	if err := Each(sessions, false, func(id string, raw []byte, err error) error {
		if err != nil {
			return err
		}
		var record map[string]json.RawMessage
		return json.Unmarshal(raw, &record)
	}); err != nil {
		t.Fatal(err)
	}
	if err := Owners(sessions, func(OwnerRow) error { return nil }); err != nil {
		t.Fatalf("ownership digest after reads and updates: %v", err)
	}
}

func TestInstructionBlobReferenceCleanup(t *testing.T) {
	type record struct {
		Instructions      string `json:"instructions"`
		InstructionsKnown string `json:"instructionsKnown"`
	}
	sessions := filepath.Join(t.TempDir(), "sessions")
	shared := record{Instructions: "shared", InstructionsKnown: "known"}
	for _, id := range []string{"a", "b", "c"} {
		if err := Save(sessions, id, shared); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM session_instruction_blobs").Scan(&got); err != nil || got != want {
			t.Fatalf("instruction blobs = %d, %v; want %d", got, err, want)
		}
	}
	count(2)
	changed := shared
	changed.Instructions = "replacement"
	if err := Save(sessions, "a", changed); err != nil {
		t.Fatal(err)
	}
	count(3) // b and c still share the replaced value.
	if err := Delete(sessions, "b"); err != nil {
		t.Fatal(err)
	}
	count(3) // c still shares the deleted session's value.
	if err := Delete(sessions, "c"); err != nil {
		t.Fatal(err)
	}
	count(2) // The shared instruction is gone; a still uses the other two.
	if err := Delete(sessions, "a"); err != nil {
		t.Fatal(err)
	}
	count(0)
}

func TestInstructionMigrationCollisionRollsBack(t *testing.T) {
	raw := `{"id":"a","instructions":{"text":"expected"},"future":{"keep":true}}`
	sessions := v3Store(t, [2]string{"a", raw})
	w, err := splitInstructions([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(instructionBlobSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_instruction_blobs(hash,body) VALUES(?,?)", w.hashes[0], `{"text":"collision"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := records(sessions); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("migration accepted a hash collision: %v", err)
	}
	var meta, version string
	if err := db.QueryRow("SELECT meta FROM session_records WHERE id='a'").Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if meta != raw {
		t.Fatal("failed migration changed the source record")
	}
	if err := db.QueryRow("SELECT value FROM meta WHERE key='sessions.records'").Scan(&version); err != nil || version != "3" {
		t.Fatalf("failed migration advanced schema version to %q, %v", version, err)
	}
	var hashColumns int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('session_records') WHERE name='instructions_hash'").Scan(&hashColumns); err != nil || hashColumns != 0 {
		t.Fatalf("failed migration left its schema change behind: %d, %v", hashColumns, err)
	}
}

func TestInstructionBlobConcurrentReads(t *testing.T) {
	type record struct {
		Epoch        int `json:"epoch"`
		Instructions struct {
			Epoch int    `json:"epoch"`
			Text  string `json:"text"`
		} `json:"instructions"`
	}
	sessions := filepath.Join(t.TempDir(), "sessions")
	initial := record{Epoch: 0}
	initial.Instructions.Text = strings.Repeat("x", 16<<10)
	if err := Save(sessions, "reader", initial); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= 20; i++ {
			next := record{Epoch: i}
			next.Instructions.Epoch = i
			next.Instructions.Text = strings.Repeat("x", 16<<10)
			if err := Save(sessions, "reader", next); err != nil {
				errs <- err
				return
			}
		}
	}()
	for _, each := range []bool{false, true} {
		each := each
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 30; i++ {
				check := func(raw []byte) error {
					var got record
					if err := json.Unmarshal(raw, &got); err != nil {
						return err
					}
					if got.Epoch != got.Instructions.Epoch {
						return fmt.Errorf("hot epoch %d with instruction epoch %d", got.Epoch, got.Instructions.Epoch)
					}
					return nil
				}
				if each {
					if err := Each(sessions, false, func(_ string, raw []byte, err error) error {
						if err != nil {
							return err
						}
						return check(raw)
					}); err != nil {
						errs <- err
						return
					}
				} else {
					raw, _, err := LoadRaw(sessions, "reader")
					if err != nil {
						errs <- err
						return
					}
					if err := check(raw); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
