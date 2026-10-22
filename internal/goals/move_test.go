package goals

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
)

func TestMoveItemTransaction(t *testing.T) {
	for _, mode := range []string{"move", "promote", "long", "unassigned"} {
		t.Run(mode, func(t *testing.T) {
			s := itemStore(t)
			sessions := filepath.Join(filepath.Dir(s.Root), "sessions")
			const other = "ses_20261017-120001-abc123"
			text := "Fix placement\n\nBrief:\nKeep the complete brief."
			if mode == "long" {
				text = strings.Repeat("界", 2100) + "\n\nBrief:\nKeep the complete brief."
			}
			assignee := lifecycleHolder
			if mode == "unassigned" {
				assignee = ""
			}
			recordtest.Write(t, sessions, lifecycleHolder, map[string]any{"id": lifecycleHolder, "roomId": "r", "goal": "g1", "item": "i1", "parentSession": other, "extra": map[string]any{"keep": "yes", "big": int64(9007199254740993)}})
			recordtest.Write(t, sessions, other, map[string]any{"id": other, "roomId": "r", "goal": "g1", "item": "i99"})
			it, err := s.AddItem("r", "g1", "owner", text, assignee)
			if err != nil {
				t.Fatal(err)
			}
			otherBefore, _ := recordtest.ReadRaw(t, sessions, other)
			version, _ := sessionlife.RecordVersion(sessions, lifecycleHolder)
			wantExtra := "yes"
			if mode == "move" {
				// A metadata edit after the first transaction pass must survive
				// the retry that takes this session's lock.
				updated := false
				t.Cleanup(sessionlife.SetBeforeSessionLock(func(id string) {
					if id == lifecycleHolder && !updated {
						updated = true
						wantExtra = "updated"
						recordtest.Write(t, sessions, lifecycleHolder, map[string]any{"id": lifecycleHolder, "roomId": "r", "goal": "g1", "item": "i1", "parentSession": other, "extra": map[string]any{"keep": wantExtra, "big": int64(9007199254740993)}})
					}
				}))
			}
			dest, err := s.Add("r", "owner", Input{Text: "Destination", Holders: []string{"owner"}})
			if err != nil {
				t.Fatal(err)
			}
			to := dest.ID
			if mode != "move" {
				to = "new"
			}
			revision, _ := s.Revision()
			moved, err := s.MoveItem("r", "g1", it.ID, "owner", to)
			if err != nil {
				t.Fatal(err)
			}
			if moved.From.Goal != "g1" || moved.From.Item != it.ID || moved.To.Goal == "g1" {
				t.Fatalf("references = %+v", moved)
			}
			if fresh, _ := s.Revision(); fresh == revision {
				t.Fatal("goal revision did not change")
			}
			if old, err := s.Items("r", "g1"); err != nil || len(old) != 0 {
				t.Fatalf("source items = %+v, %v", old, err)
			}
			if mode == "move" {
				items, err := s.Items("r", moved.To.Goal)
				if err != nil || len(items) != 1 || items[0].Text != text || items[0].Assignee != assignee || items[0].State != ItemOpen || items[0].ID == it.ID || items[0].ID != moved.To.Item {
					t.Fatalf("destination items = %+v, %v", items, err)
				}
			} else {
				g, err := s.Get("r", moved.To.Goal)
				holder := assignee
				if holder == "" {
					holder = "owner"
				}
				if err != nil || !g.Open() || !g.HeldBy(holder) || g.Setter != "owner" || moved.To.Item != "" || utf8.RuneCountInString(g.Text) > 2000 {
					t.Fatalf("promoted goal = %+v, %v", g, err)
				}
				if mode == "long" {
					if g.Detail != text {
						t.Fatal("promotion lost the long outcome or brief")
					}
				} else if g.Text+"\n\nBrief:\n"+g.Detail != text {
					t.Fatal("promotion lost the outcome or brief")
				}
			}
			var meta struct {
				Goal, Item, ParentSession string
				Extra                     map[string]any
			}
			// Match the pair, including when it is no longer the assignee.
			recordtest.Read(t, sessions, lifecycleHolder, &meta)
			if meta.Goal != moved.To.Goal || meta.Item != moved.To.Item || meta.ParentSession != other || meta.Extra["keep"] != wantExtra {
				t.Fatalf("metadata = %+v", meta)
			}
			raw, _ := recordtest.ReadRaw(t, sessions, lifecycleHolder)
			if !strings.Contains(string(raw), "9007199254740993") {
				t.Fatal("unrelated metadata lost precision")
			}
			if fresh, _ := sessionlife.RecordVersion(sessions, lifecycleHolder); fresh == version {
				t.Fatal("session revision did not change")
			}
			if raw, _ := recordtest.ReadRaw(t, sessions, other); string(raw) != string(otherBefore) {
				t.Fatal("unrelated assignment changed")
			}
			st, _ := s.Read()
			notices := st.Room("r").Notices
			if assignee == "" {
				if len(notices) != 0 {
					t.Fatalf("unassigned item sent a notice: %+v", notices)
				}
			} else if len(notices) != 1 || notices[0].To != assignee || !strings.Contains(notices[0].Text, moved.To.Goal) || !strings.Contains(notices[0].Text, "dyna goal ") || strings.Contains(notices[0].Text, "dropped") {
				t.Fatalf("placement notice = %+v", notices)
			}
			events, err := s.Events("r", "", 0)
			if err != nil || len(events) != 3 || events[0].Goal != moved.To.Goal || events[1].Goal != "g1" || events[2].Event != "added" {
				t.Fatalf("history = %+v, %v", events, err)
			}
			if !strings.Contains(events[0].Text, "g1/"+it.ID) || events[0].Text != events[1].Text {
				t.Fatalf("missing cross links: %+v", events)
			}
		})
	}
}

func TestMoveItemRollback(t *testing.T) {
	for _, mode := range []string{"missing", "other room", "closed destination", "closed source", "done item", "same goal", "metadata failure"} {
		t.Run(mode, func(t *testing.T) {
			s := itemStore(t)
			sessions := filepath.Join(filepath.Dir(s.Root), "sessions")
			recordtest.Write(t, sessions, lifecycleHolder, map[string]any{"id": lifecycleHolder, "roomId": "r", "goal": "g1", "item": "i1"})
			if _, err := s.AddItem("r", "g1", "owner", "Move me", lifecycleHolder); err != nil {
				t.Fatal(err)
			}
			dest, err := s.Add("r", "owner", Input{Text: "Destination"})
			if err != nil {
				t.Fatal(err)
			}
			to := dest.ID
			switch mode {
			case "missing":
				to = "g99"
			case "other room":
				for range 3 {
					g, err := s.Add("other", "owner", Input{Text: "Other room"})
					if err != nil {
						t.Fatal(err)
					}
					to = g.ID
				}
			case "closed destination":
				_, err = s.Finish("r", to, "owner", "Done", false)
			case "closed source":
				_, err = s.Finish("r", "g1", "owner", "Done", false)
			case "done item":
				_, err = s.SetItemState("r", "g1", "i1", "owner", ItemDone)
			case "same goal":
				to = "g1"
			case "metadata failure":
				to = "new"
				db, openErr := openGoalDB(s)
				if openErr != nil {
					t.Fatal(openErr)
				}
				err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(context.Background(), "CREATE TRIGGER fail_move BEFORE UPDATE ON session_records BEGIN SELECT RAISE(ABORT, 'metadata failure'); END")
					return err
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.Read()
			items, _ := s.Items("r", "g1")
			raw, _ := recordtest.ReadRaw(t, sessions, lifecycleHolder)
			revision, _ := s.Revision()
			events, _ := s.Events("r", "", 0)
			if _, err := s.MoveItem("r", "g1", "i1", "owner", to); err == nil {
				t.Fatal("invalid move succeeded")
			}
			after, _ := s.Read()
			freshItems, _ := s.Items("r", "g1")
			freshRaw, _ := recordtest.ReadRaw(t, sessions, lifecycleHolder)
			freshRevision, _ := s.Revision()
			freshEvents, _ := s.Events("r", "", 0)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(items, freshItems) || string(raw) != string(freshRaw) || revision != freshRevision || !reflect.DeepEqual(events, freshEvents) {
				t.Fatal("failed move changed persistent state")
			}
		})
	}
}
