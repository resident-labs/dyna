package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/questions"
)

func TestRoomClearUsesActualManagerGoalAndQuestionStores(t *testing.T) {
	for _, kind := range []string{"goal", "question", "bad-goals", "bad-questions"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			manager := NewManager()
			e := roomControlEnv(t)
			a, _ := e.start("SLEEP:1 RESPOND: idle")
			e.wait(a.ID, 1)
			e.m.Store.UpdateMeta(a.ID, func(m *Meta) {
				m.LastActivity, m.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				m.Status = StatusDormant
			})
			switch kind {
			case "goal":
				if _, err := e.m.goalStore().Add(a.RoomID, "owner", goals.Input{Text: "unfinished", Holders: []string{a.ID}}); err != nil {
					t.Fatal(err)
				}
			case "question":
				if _, err := e.m.questionStore().Ask(questions.Question{Room: a.RoomID, Asker: a.ID, To: "owner", Text: "still needed?", Meanwhile: "continue"}, nil); err != nil {
					t.Fatal(err)
				}
			default:
				root := e.m.goalStore().Root
				if kind == "bad-questions" {
					root = e.m.questionStore().Root
				}
				os.MkdirAll(root, 0700)
				os.WriteFile(filepath.Join(root, "state.json"), []byte("broken"), 0600)
			}
			// Construction precedes replacement of Store, as in services and embedders.
			manager.Store = &Store{Root: e.m.Store.Root}
			manager.Rooms = e.m.RoomStore()
			result, err := closeIdle(manager, e.ctx, a.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			meta, _ := manager.Store.ReadMeta(a.ID)
			if kind == "goal" || kind == "question" {
				if meta.Status != StatusIdle || !meta.Finished.IsZero() || len(result.Results) != 0 || result.Succeeded != 0 {
					t.Fatalf("work owner did not leave dormancy: %+v, %+v", meta, result)
				}
				return
			}
			if !meta.Closed.IsZero() || result.Succeeded != 0 || len(result.Results) != 1 || result.Results[0].Status != "kept" {
				t.Fatalf("lost protected work: %+v", result)
			}
		})
	}
}
