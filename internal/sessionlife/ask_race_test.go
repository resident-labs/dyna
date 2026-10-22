package sessionlife_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/sessionlife"
)

// File locks come before the dyna.db transaction, never inside it. A record
// write holds its session's lock and then needs the database writer; Ask
// needs both too, so it must take the session lock first. In the other order
// Ask holds the writer while it waits for the lock, and neither finishes.
func TestAskWhileARecordWriteHoldsTheSessionLock(t *testing.T) {
	data := t.TempDir()
	sessions := filepath.Join(data, "sessions")
	id := "ses_20261020-000000-abcdef12"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"id": id, "roomId": "room_1", "room": "work", "status": "dormant"}
	if err := sessionlife.Save(sessions, id, record); err != nil {
		t.Fatal(err)
	}
	holding, release := make(chan struct{}), make(chan struct{})
	written := make(chan error, 1)
	go func() {
		written <- sessionlife.WithSessionLocks(sessions, []string{id}, func() error {
			close(holding)
			<-release
			return sessionlife.Save(sessions, id, record)
		})
	}()
	<-holding
	asking := make(chan struct{})
	defer sessionlife.SetBeforeSessionLock(func(string) { close(asking) })()
	asked := make(chan error, 1)
	store := &questions.Store{Root: filepath.Join(data, "questions")}
	go func() {
		_, err := store.Ask(questions.Question{Room: "room_1", Asker: id, To: "owner", Text: "still needed?", Meanwhile: "continue"}, nil)
		asked <- err
	}()
	<-asking // Ask is about to wait for the session lock the writer holds.
	close(release)
	// The timeouts only bound a deadlock; the order of events is fixed above.
	for name, done := range map[string]chan error{"record write": written, "Ask": asked} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s deadlocked: a file lock was taken inside a dyna.db transaction", name)
		}
	}
	var after struct{ Status string }
	if err := sessionlife.Load(sessions, id, &after); err != nil || after.Status != "idle" {
		t.Fatalf("record after Ask = %+v, %v; want the asker woken (idle)", after, err)
	}
}

// On the first run after the upgrade, the record table does not exist yet.
// Ask must create it before its own transaction, not inside it.
func TestAskOnAFreshDataFolderFinishes(t *testing.T) {
	data := t.TempDir()
	sessions := filepath.Join(data, "sessions")
	id := "ses_20261020-000000-abcdef13"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"` + id + `","roomId":"room_1","room":"work","status":"dormant"}`
	if err := os.WriteFile(filepath.Join(sessions, id, "session.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &questions.Store{Root: filepath.Join(data, "questions")}
	asked := make(chan error, 1)
	go func() {
		_, err := store.Ask(questions.Question{Room: "room_1", Asker: id, To: "owner", Text: "still needed?", Meanwhile: "continue"}, nil)
		asked <- err
	}()
	select {
	case err := <-asked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask on a fresh data folder deadlocked")
	}
}

// Posting a question takes the asker's session lock (to record the post on
// its turn). Meanwhile a metadata write may hold that lock and wait for the
// database writer. Ask must therefore post outside its store transaction.
func TestAskPostsOutsideItsTransaction(t *testing.T) {
	data := t.TempDir()
	sessions := filepath.Join(data, "sessions")
	id := "ses_20261020-000000-abcdef14"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"id": id, "roomId": "room_1", "room": "work", "status": "idle"}
	if err := sessionlife.Save(sessions, id, record); err != nil {
		t.Fatal(err)
	}
	posting, holding := make(chan struct{}), make(chan struct{})
	written := make(chan error, 1)
	post := func(questions.Question) (string, error) {
		close(posting)
		<-holding // A metadata write now holds the asker's lock and needs the writer.
		// What Store.UpdateTurn does: take the asker's session lock.
		return "post_1", sessionlife.WithSessionLocks(sessions, []string{id}, func() error { return nil })
	}
	store := &questions.Store{Root: filepath.Join(data, "questions")}
	asked := make(chan error, 1)
	go func() {
		_, err := store.Ask(questions.Question{Room: "room_1", Asker: id, To: "owner", Text: "still needed?", Meanwhile: "continue"}, post)
		asked <- err
	}()
	<-posting
	go func() {
		written <- sessionlife.WithSessionLocks(sessions, []string{id}, func() error {
			close(holding)
			return sessionlife.Save(sessions, id, record)
		})
	}()
	for name, done := range map[string]chan error{"metadata write": written, "Ask": asked} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s deadlocked: Ask posted inside its store transaction", name)
		}
	}
	qs, err := store.List("room_1")
	if err != nil || len(qs) != 1 || qs[0].Post != "post_1" {
		t.Fatalf("questions after Ask = %+v, %v; want one with its post", qs, err)
	}
}
