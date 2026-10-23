package roomsvc

import (
	"context"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/state"
)

func TestStoreMutationPublishesOnlyAfterCommit(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	g := &goals.Store{Root: filepath.Join(dir, "goals")}
	q := &questions.Store{Root: filepath.Join(dir, "questions")}
	n := notices.Store{Dir: dir}
	sync := &lanesync.Service{Dir: dir}
	if _, err := g.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := n.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := sync.Read(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writes := []struct {
		topic, table string
		write        func() error
	}{
		{"goals", "goals", func() error { _, err := g.Add("room", "owner", goals.Input{Text: "A goal", Shared: true}); return err }},
		{"questions", "questions", func() error {
			_, err := q.Ask(questions.Question{Room: "room", Asker: "owner", Text: "A question?", Meanwhile: "Read code"}, nil)
			return err
		}},
		{"notices", "notices", func() error {
			_, err := n.Post(notices.Notice{Key: "new", Kind: notices.KindDisk, Severity: notices.SeverityError, Disk: &notices.DiskFacts{Paths: []notices.DiskPath{{Path: "/tmp", FreeBytes: 1024}}}})
			return err
		}},
		{"sync", "lane_sync", func() error {
			return sync.Update(func(st *lanesync.Snapshot) error { st.Events["new"] = "committed"; return nil })
		}},
	}
	for _, w := range writes {
		events := 0
		state.For(db).SetPublish(func(ev hub.Event) {
			if ev.Topic != w.topic {
				t.Errorf("topic = %s; want %s", ev.Topic, w.topic)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + w.table).Scan(&count); err != nil || count != 1 {
				t.Errorf("event before commit: count=%d, err=%v", count, err)
			}
			events++
		})
		if err := w.write(); err != nil {
			t.Fatal(err)
		}
		if events != 1 {
			t.Fatalf("%s published %d events; want 1", w.topic, events)
		}
		operation := "INSERT"
		if w.topic == "sync" {
			operation = "UPDATE"
		}
		if _, err := db.Exec("CREATE TRIGGER refuse_" + w.table + " BEFORE " + operation + " ON " + w.table + " BEGIN SELECT RAISE(ABORT,'refused'); END"); err != nil {
			t.Fatal(err)
		}
		if err := w.write(); err == nil {
			t.Fatalf("%s ignored rollback trigger", w.topic)
		}
		if events != 1 {
			t.Fatalf("%s published rolled back data", w.topic)
		}
	}
	state.For(db).SetPublish(nil)
	// Bursts retain each changed topic once until the watcher consumes them.
	service := New(nil)
	for _, topic := range []string{"goals", "questions", "goals", "notices", "sync"} {
		service.StoreChanged(topic)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Assert a wake exists before entering the tick-free loop.
	if len(service.storeWake) != 1 {
		t.Fatal("store changes did not coalesce into one wake")
	}
	scans := 0
	unexpected := func() { t.Fatal("store wake scanned an unrelated source") }
	if service.waitTick(ctx, nil, unexpected, unexpected, unexpected, unexpected, func() {
		topics := service.takeStoreTopics()
		if len(topics) != 4 || !topics["goals"] || !topics["questions"] || !topics["notices"] || !topics["sync"] {
			t.Errorf("pending topics = %v", topics)
		}
		scans++
		cancel()
	}) || scans != 1 {
		t.Fatalf("store wake scans = %d; want 1", scans)
	}
}
