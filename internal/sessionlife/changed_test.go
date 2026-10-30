package sessionlife

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

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
