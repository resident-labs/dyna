package sessionlife

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestUnchangedSaveKeepsRevisionAndRequest(t *testing.T) {
	dataDir := t.TempDir()
	sessions := filepath.Join(dataDir, "sessions")
	if err := state.MigrateRequests(dataDir); err != nil {
		t.Fatal(err)
	}
	id := "ses_20261020-000000-abcdef12"
	db, err := records(sessions)
	if err != nil {
		t.Fatal(err)
	}
	writer := state.For(db)
	var got []hub.Event
	writer.SetPublish(func(ev hub.Event) { got = append(got, ev) })
	t.Cleanup(func() { writer.SetPublish(nil) })
	value := struct {
		ID string `json:"id"`
	}{id}
	if err := Save(sessions, id, value); err != nil {
		t.Fatal(err)
	}
	before, ok := RecordVersion(sessions, id)
	if !ok {
		t.Fatal("missing record version")
	}
	ctx := state.ForRequest(context.Background(), "same-save")
	if err := SaveContext(ctx, sessions, id, value); err != nil {
		t.Fatal(err)
	}
	after, ok := RecordVersion(sessions, id)
	if !ok || after != before {
		t.Fatalf("version = %q, %v; want %q", after, ok, before)
	}
	writer.WaitPublished()
	if len(got) != 1 {
		t.Fatalf("events = %v; want one", got)
	}
	if err := SaveContext(state.ForRequest(context.Background(), "same-save"), sessions, id, value); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed request = %v", err)
	}
}

// A session write publishes one sessions event that names the session, so
// the engine can wake its room watch for that session alone.
func TestSessionWritesNameTheSession(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	id := "ses_20261020-000000-abcdef12"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := records(sessions)
	if err != nil {
		t.Fatal(err)
	}
	var got []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) { got = append(got, ev) })
	t.Cleanup(func() { state.For(db).SetPublish(nil) })
	if err := Save(sessions, id, map[string]any{"id": id, "turns": 1}); err != nil {
		t.Fatal(err)
	}
	if err := SaveIf(sessions, id, map[string]any{"id": id, "turns": 2}, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Delete(sessions, id); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("events = %+v; want one per write", got)
	}
	for _, ev := range got {
		if ev.Topic != Topic || ev.Key != id {
			t.Fatalf("event = %+v; want topic %q for %s", ev, Topic, id)
		}
	}
}
