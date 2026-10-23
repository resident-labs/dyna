package roomsvc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
)

func TestWebReadsDoNotRepairSessionsOrChangeRoomFiles(t *testing.T) {
	reads := map[string]func(*env, string) error{
		"Open":      func(e *env, id string) error { _, err := e.svc.Open(id, 50, false); return err },
		"FeedTail":  func(e *env, id string) error { _, err := e.svc.FeedTail(id, "", 50); return err },
		"Questions": func(e *env, id string) error { _, err := e.svc.Questions(id); return err },
		"AllQuestions": func(e *env, id string) error {
			grouped, err := e.svc.Questions("")
			if err == nil && len(grouped[id]) != 1 {
				return fmt.Errorf("all-rooms read returned %d questions for fixture room", len(grouped[id]))
			}
			return err
		},
		"Runs":      func(e *env, id string) error { _, err := e.svc.Runs(id); return err },
		"Members":   func(e *env, id string) error { _, err := e.svc.Members(id, false); return err },
		"FeedSince": func(e *env, id string) error { _, _, err := e.svc.FeedSince(id, 0); return err },
		"Rooms":     func(e *env, id string) error { _, err := e.svc.Rooms(); return err },
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("test-room")
			e.say(rec, "lead", "A retained post")
			id := "ses_20261017-120000-aabbccdd"
			activityRecord(t, e.mgr.Store.Root, id, map[string]any{"id": id, "name": "orphan", "roomId": rec.ID, "turns": 1}, []map[string]any{{"status": "running", "started": time.Now()}})
			if _, err := e.svc.Notify.QuestionStore().Ask(questions.Question{Room: rec.ID, Asker: id, Text: "A retained question", Meanwhile: "Check other work"}, nil); err != nil {
				t.Fatal(err)
			}
			writeRun(t, runstore.Meta{ID: "wf_20261017-120000-aabbccdd", Name: "legacy-run", Status: "ok", ParentSession: id, StartedAt: time.Now()}, nil, "")
			repairs := 0
			report := e.mgr.Store.ReportRepaired
			e.mgr.Store.ReportRepaired = func(store *subagent.Store, id string, n int) { repairs++; report(store, id, n) }
			// Retained files remain untouched even when they are stale.
			for _, name := range []string{"room.json", "seat.json"} {
				if err := os.WriteFile(filepath.Join(e.svc.rooms().Root, rec.ID, name), []byte("{stale"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{
				filepath.Join(e.svc.rooms().Root, rec.ID, "room.json"),
				filepath.Join(e.svc.rooms().Root, rec.ID, "seat.json"),
				filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl"),
				filepath.Join(e.mgr.Store.Root, id, "turns", "1.json"),
			}
			before := map[string][]byte{}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = data
			}
			if err := read(e, rec.ID); err != nil {
				t.Fatal(err)
			}
			if repairs != 0 {
				t.Errorf("read ran %d session repairs", repairs)
			}
			for _, path := range paths {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before[path], after) {
					t.Errorf("read changed %s", filepath.Base(path))
				}
			}
		})
	}
}
