package goal

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestJournalCommandShowsTailOldestFirst(t *testing.T) {
	storetest.ClearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(root, "sessions")}
	mgr.Rooms = rooms
	s := goalsvc.New()
	s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
	s.Notify = notify.New(rooms, mgr)
	var ids []string
	for _, status := range []string{"on_track", "waiting", "blocked"} {
		entry, err := s.Store.AddJournal(rec.ID, goals.JournalEntry{Entries: []goals.JournalLine{{Agent: "work:lead", Status: status, Summary: "Status " + status, Goals: []string{"g1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, entry.ID)
	}
	run := func(args ...string) string {
		cmd := Command(Options{Service: func() *goalsvc.Service { return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append(args, "--room", rec.Name))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	human := run("journal", "--tail", "2")
	if strings.Contains(human, ids[0]+" ") || strings.Index(human, ids[1]+" ") > strings.Index(human, ids[2]+" ") || !strings.Contains(human, ids[2]+" ") || !strings.Contains(human, "needs action") || !strings.Contains(human, "work:lead · blocked · Status blocked (g1)") {
		t.Fatal(human)
	}
	var entries []goals.JournalEntry
	if err := json.Unmarshal([]byte(run("journal", "--json")), &entries); err != nil || len(entries) != 3 || entries[0].ID != ids[0] {
		t.Fatalf("%+v %v", entries, err)
	}
}
