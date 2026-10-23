package subagent

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

func TestStoredPromptRewritePreservesTextAndRollsBack(t *testing.T) {
	s, id := turnStore(t)
	ctx := t.Context()
	if err := s.writeMeta(Meta{ID: id, RoomID: "room", Room: "room", Turns: 2}); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("shared prompt ", 10)
	hash := func(text string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))) }
	literal := hash(text)
	for n := 1; n <= 2; n++ {
		if err := s.writeTurn(id, Turn{
			N: n, Status: TurnOK, Prompt: text, HeldPrompt: literal,
			Steering:   []Steer{{Message: "retained steering", Delivered: true}},
			Deliveries: []Delivery{{Outcome: "bound-hit"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	checkStorage := func() {
		t.Helper()
		raw, _, err := storedTurn(ctx, db, id, 1)
		if err != nil {
			t.Fatal(err)
		}
		var stored Turn
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Prompt != hash(text) || stored.HeldPrompt != hash(literal) {
			t.Fatalf("stored prompts = %q, %q; want references to full text", stored.Prompt, stored.HeldPrompt)
		}
		if blobs, refs := count("SELECT count(*) FROM prompt_blobs"), count("SELECT count(*) FROM prompt_refs"); blobs != 2 || refs != 4 {
			t.Fatalf("blobs = %d, references = %d; want 2 and 4", blobs, refs)
		}
	}
	checkStorage()
	checkText := func(turn Turn, err error) {
		t.Helper()
		if err != nil || turn.Prompt != text || turn.HeldPrompt != literal {
			t.Fatalf("resolved prompts = %q, %q, %v", turn.Prompt, turn.HeldPrompt, err)
		}
	}
	checkText(s.ReadTurn(id, 1))
	turns, err := s.Turns(id)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turn listing = %v, %v", turns, err)
	}
	for _, turn := range turns {
		checkText(turn, nil)
	}
	snapshots, err := s.SteeringAnchorSnapshots(ctx, id)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("steering snapshots = %v, %v", snapshots, err)
	}
	checkText(snapshots[0].Turn, nil)

	before, revision, err := storedTurn(ctx, db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM prompt_blobs WHERE hash=?", hash(text)); err != nil {
		t.Fatal(err)
	}
	anchor := int64(0)
	rewrite := func() error {
		return state.For(db).Write(ctx, sessionlife.Changed(id), func(conn *sql.Conn) error {
			return s.SetSteeringAnchors(ctx, conn, id, snapshots[0], []*int64{&anchor})
		})
	}
	migrations := []struct {
		key string
		run func() error
	}{
		{"subagent.turns.delivery_reason", s.migrateDeliveryReasons},
		{"subagent.turns.cutoff", s.migrateTurnCutoffs},
		{"subagent.turns.usage", s.migrateRetainedUsage},
	}
	actions := []func() error{rewrite}
	for _, migration := range migrations {
		actions = append(actions, migration.run)
	}
	for _, action := range actions {
		if err := action(); err == nil || !strings.Contains(err.Error(), "is missing") {
			t.Fatalf("rewrite with a missing prompt blob = %v", err)
		}
		after, current, err := storedTurn(ctx, db, id, 1)
		if err != nil || current != revision || !bytes.Equal(before, after) {
			t.Fatalf("failed rewrite changed row or revision: %d -> %d, %v", revision, current, err)
		}
		if count("SELECT count(*) FROM prompt_refs") != 4 {
			t.Fatal("failed rewrite changed prompt references")
		}
	}
	for _, migration := range migrations {
		if count("SELECT count(*) FROM meta WHERE key=?", migration.key) != 0 {
			t.Fatalf("failed rewrite recorded %s", migration.key)
		}
	}
	turns, err = s.Turns(id)
	if err != nil || len(turns) != 2 || turns[0].Status != TurnUnreadable || turns[1].Status != TurnUnreadable {
		t.Fatalf("missing blob listing = %v, %v", turns, err)
	}
	if _, err := db.Exec("INSERT INTO prompt_blobs(hash,text) VALUES (?,?)", hash(text), text); err != nil {
		t.Fatal(err)
	}
	if err := rewrite(); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if err := migration.run(); err != nil {
			t.Fatal(err)
		}
		checkText(s.ReadTurn(id, 1))
		checkStorage()
	}
	updated, err := s.ReadTurn(id, 1)
	checkText(updated, err)
	if updated.Steering[0].SourceOffset == nil || *updated.Steering[0].SourceOffset != anchor {
		t.Fatal("retry did not save the steering anchor")
	}

	// Unconverted inline text can equal another turn's actual reference.
	inline, _ := json.Marshal(Turn{N: 3, Prompt: literal})
	if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES (?,3,100,?)", id, inline); err != nil {
		t.Fatal(err)
	}
	if turn, err := s.ReadTurn(id, 3); err != nil || turn.Prompt != literal {
		t.Fatalf("unconverted inline prompt = %q, %v", turn.Prompt, err)
	}
	// The one-time retained-file import also treats hashes as literal text.
	legacy, legacyID := turnStore(t)
	raw, _ := json.Marshal(Turn{N: 1, Prompt: literal})
	if err := os.WriteFile(turnFile(t, legacy, legacyID, 1), raw, 0600); err != nil {
		t.Fatal(err)
	}
	turn, err := legacy.ReadTurn(legacyID, 1)
	if err != nil || turn.Prompt != literal {
		t.Fatalf("imported prompt = %q, %v", turn.Prompt, err)
	}
	legacyDB, err := legacy.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := storedTurn(ctx, legacyDB, legacyID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored, &turn); err != nil || turn.Prompt != hash(literal) {
		t.Fatalf("imported reference = %q, %v", turn.Prompt, err)
	}
}
