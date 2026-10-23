package stores

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

func TestWithdrawalRoutingIsFixedByStartupMigration(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	rooms := &room.Store{Root: filepath.Join(dir, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rooms", rec.ID, "messages.jsonl")
	source := []byte("{\"id\":\"original\",\"roomID\":\"" + rec.ID + "\",\"from\":\"alpha:asker\",\"to\":[\"alpha:lead\"],\"kind\":\"message\",\"text\":\"[QUESTION q1] Which?\"}\n" +
		"{\"id\":\"withdrawal\",\"roomID\":\"" + rec.ID + "\",\"from\":\"alpha\",\"kind\":\"question-withdrawn\",\"text\":\"q1 withdrawn: done\"}\n")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := questions.Question{ID: "q1", Room: rec.ID, Asker: "asker", Post: "original", Withdrawn: &questions.Withdraw{By: "owner"}}
	body, _ := json.Marshal(q)
	if _, err := db.Exec("INSERT INTO questions(key,room,state,asker,body) VALUES(?,?,?,?,?)", rec.ID+"/q1", rec.ID, "withdrawn", "asker", string(body)); err != nil {
		t.Fatal(err)
	}
	// Replay only this startup step after seeding retained history.
	if _, err := db.Exec("DELETE FROM meta WHERE key='room.posts.question_withdrawals'"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		posts, _, err := rooms.ReadAll(rec.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(posts) != 2 {
			t.Fatalf("posts = %+v", posts)
		}
		m := posts[1]
		if m.From != "alpha" || m.Kind != "message" || m.ReplyTo != "original" || m.Text != "[WITHDRAWN q1] done" || len(m.To) != 2 || m.To[0] != "alpha:lead" || m.To[1] != "alpha:asker" {
			t.Fatalf("withdrawal = %+v", m)
		}
	}
	check()
	if _, err := db.Exec("UPDATE questions SET body='{}'"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	check()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, source) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}
