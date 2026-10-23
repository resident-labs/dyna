package subagent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
)

func TestMigratePersistsRetainedTurnInputCutoff(t *testing.T) {
	for _, disorder := range []bool{false, true} {
		t.Run(map[bool]string{false: "prefix", true: "non-prefix"}[disorder], func(t *testing.T) {
			s, id := turnStore(t)
			rooms := &room.Store{Root: filepath.Join(filepath.Dir(s.Root), "rooms")}
			rec, err := rooms.Create(room.CreateInput{Name: "cutoff"})
			if err != nil {
				t.Fatal(err)
			}
			meta := Meta{ID: id, RoomID: rec.ID, Room: rec.Name, Turns: 1, Resumable: true, HarnessSession: "native"}
			if err := s.writeMeta(meta); err != nil {
				t.Fatal(err)
			}
			ended := time.UnixMilli(10000)
			post := func(at time.Time) room.Message {
				t.Helper()
				rooms.Now = func() time.Time { return at }
				m, err := rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{meta.Address()}, Text: "input", Wake: true})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			post(ended.Add(-time.Millisecond))
			post(ended)
			cutoff, err := rooms.FileSize(rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			fresh := post(ended.Add(time.Millisecond))
			if disorder {
				post(ended)
			}
			logPath := filepath.Join(rooms.Root, rec.ID, "messages.jsonl")
			logBefore, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			preserved := int64(7)
			if err := s.writeTurn(id, Turn{N: 2, Status: TurnStopped, InputCutoff: &preserved}); err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"n":1,"status":"error","ended":"1970-01-01T00:00:10Z","future":{"kept":true},"usageNormalization":{"version":1}}`)
			path := turnFile(t, s, id, 1)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.migrateRetainedTurns(); err != nil {
				t.Fatal(err)
			}
			db, err := s.turnsDB()
			if err != nil {
				t.Fatal(err)
			}
			_, before, err := storedTurn(t.Context(), db, id, 1)
			if err != nil {
				t.Fatal(err)
			}
			err = Migrate(filepath.Dir(s.Root))
			if disorder {
				if err == nil {
					t.Fatal("accepted unrepresentable prefix")
				}
				_, after, _ := storedTurn(t.Context(), db, id, 1)
				if before != after {
					t.Fatal("failed migration changed turn")
				}
				var version string
				err = db.QueryRow("SELECT value FROM meta WHERE key='subagent.turns.cutoff'").Scan(&version)
				if err == nil {
					t.Fatal("failed migration saved version")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			turn, err := s.ReadTurn(id, 1)
			if err != nil || turn.InputCutoff == nil || *turn.InputCutoff != cutoff {
				t.Fatalf("cutoff = %v, %v; want %d", turn.InputCutoff, err, cutoff)
			}
			posts, _, err := rooms.ReadAll(rec.ID, *turn.InputCutoff)
			if err != nil || len(posts) != 1 || posts[0].ID != fresh.ID {
				t.Fatalf("fresh posts = %v, %v", posts, err)
			}
			body, revision, err := storedTurn(t.Context(), db, id, 1)
			if err != nil || !bytes.Contains(body, []byte(`"future":{"kept":true}`)) {
				t.Fatalf("unknown field lost: %s, %v", body, err)
			}
			source, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(source, raw) {
				t.Fatal("source changed")
			}
			kept, err := s.ReadTurn(id, 2)
			if err != nil || kept.InputCutoff == nil || *kept.InputCutoff != preserved {
				t.Fatalf("existing cutoff changed: %+v, %v", kept, err)
			}
			logAfter, err := os.ReadFile(logPath)
			if err != nil || !bytes.Equal(logBefore, logAfter) {
				t.Fatal("message log changed")
			}
			post(ended.Add(2 * time.Millisecond))
			if err := database.Close(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			db, err = s.turnsDB()
			if err != nil {
				t.Fatal(err)
			}
			_, again, err := storedTurn(t.Context(), db, id, 1)
			if err != nil || again != revision {
				t.Fatalf("replay changed revision %v to %v: %v", revision, again, err)
			}
			manager := &Manager{Store: s, Rooms: rooms}
			if _, err := manager.pendingWakePosts(meta, Turn{N: 1, Status: TurnError}); err == nil || !strings.Contains(err.Error(), "has no input cutoff") {
				t.Fatalf("missing cutoff accepted: %v", err)
			}
		})
	}
}
