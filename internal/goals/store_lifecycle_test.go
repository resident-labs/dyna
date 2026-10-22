package goals

import (
	"dyna-agent/internal/sessionlife"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
)

const lifecycleHolder = "ses_20261017-120000-abc123"

func lifecycleStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: filepath.Join(t.TempDir(), "goals")}
}

func writeHolder(t *testing.T, s *Store, contents string) {
	t.Helper()
	recordtest.WriteRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), lifecycleHolder, []byte(contents))
}

func TestNewHolderLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		allowed        bool
	}{
		{"open", `{"id":"` + lifecycleHolder + `"}`, true},
		{"missing", "", false},
		{"invalid", `{`, false},
		{"mismatched", `{"id":"another"}`, false},
		{"closed", `{"id":"` + lifecycleHolder + `","closed":"2026-10-17T12:00:00Z"}`, false},
		{"closing", `{"id":"` + lifecycleHolder + `","closing":{"token":"check","pid":1}}`, false},
	} {
		for _, edit := range []bool{false, true} {
			name := tc.name + "/add"
			if edit {
				name = tc.name + "/edit"
			}
			t.Run(name, func(t *testing.T) {
				s := lifecycleStore(t)
				if tc.metadata != "" {
					writeHolder(t, s, tc.metadata)
				}
				var err error
				if edit {
					g, addErr := s.Add("r", "owner", Input{Text: "Original", Holders: []string{"owner"}})
					if addErr != nil {
						t.Fatal(addErr)
					}
					holders := []string{lifecycleHolder}
					_, err = s.Edit("r", g.ID, "owner", "Changed", &holders, false)
				} else {
					_, err = s.Add("r", "owner", Input{Text: "Goal", Holders: []string{lifecycleHolder}})
				}
				if tc.allowed {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), lifecycleHolder) {
					t.Fatalf("expected error naming holder, got %v", err)
				}
				all, err := s.List("r")
				if err != nil {
					t.Fatal(err)
				}
				if edit {
					if len(all) != 1 || all[0].Text != "Original" || !reflect.DeepEqual(all[0].Holders, []string{"owner"}) || len(all[0].Edits) != 0 {
						t.Fatalf("failed edit changed state: %+v", all)
					}
				} else if len(all) != 0 {
					t.Fatalf("failed add changed state: %+v", all)
				}
			})
		}
	}
}

func TestEditChecksOnlyNewHolders(t *testing.T) {
	s := lifecycleStore(t)
	writeHolder(t, s, `{"id":"`+lifecycleHolder+`"}`)
	g, err := s.Add("r", "owner", Input{Text: "Goal", Holders: []string{lifecycleHolder, "owner", "external"}})
	if err != nil {
		t.Fatal(err)
	}
	writeHolder(t, s, `{"id":"`+lifecycleHolder+`","closed":"2026-10-17T12:00:00Z"}`)
	holders := []string{lifecycleHolder, "owner", "external", "other"}
	if _, err := s.Edit("r", g.ID, "owner", "Changed", &holders, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit("r", g.ID, "owner", "Text only", nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestLockedReadersWaitForWriter(t *testing.T) {
	for _, heldBy := range []bool{false, true} {
		name := "ReadLocked"
		if heldBy {
			name = "OpenHeldBy"
		}
		t.Run(name, func(t *testing.T) {
			s := lifecycleStore(t)
			g, err := s.Add("r", "owner", Input{Text: "Before", Holders: []string{"owner"}})
			if err != nil {
				t.Fatal(err)
			}
			locked, release := make(chan struct{}), make(chan struct{})
			writer := make(chan error, 1)
			go func() {
				writer <- s.Update(func(st *State) error {
					st.Room("r").Goals[g.ID].Text = "After"
					close(locked)
					<-release
					return nil
				})
			}()
			<-locked
			read := make(chan string, 1)
			go func() {
				if heldBy {
					all, err := s.OpenHeldBy("r", "owner")
					if err != nil || len(all) != 1 {
						read <- "read failed"
						return
					}
					read <- all[0].Text
				} else {
					st, err := s.ReadLocked()
					if err != nil {
						read <- err.Error()
						return
					}
					read <- st.Room("r").Goals[g.ID].Text
				}
			}()
			select {
			case got := <-read:
				close(release)
				<-writer
				t.Fatalf("read returned during transaction: %s", got)
			case <-time.After(30 * time.Millisecond):
			}
			close(release)
			if err := <-writer; err != nil {
				t.Fatal(err)
			}
			if got := <-read; got != "After" {
				t.Fatalf("read got %q, want committed state", got)
			}
		})
	}
}

func TestHolderCheckWaitsForTransaction(t *testing.T) {
	for _, edit := range []bool{false, true} {
		name := "add"
		if edit {
			name = "edit"
		}
		t.Run(name, func(t *testing.T) {
			s := lifecycleStore(t)
			writeHolder(t, s, `{"id":"`+lifecycleHolder+`"}`)
			g, err := s.Add("r", "owner", Input{Text: "Existing"})
			if err != nil {
				t.Fatal(err)
			}
			locked, release := make(chan struct{}), make(chan struct{})
			writer := make(chan error, 1)
			// The holder starts closing inside the transaction the check waits on.
			go func() {
				writer <- s.updateTx(func(w *wake, _ *State) error {
					close(locked)
					<-release
					return sessionlife.SaveTx(w.conn, lifecycleHolder, json.RawMessage(`{"id":"`+lifecycleHolder+`","closing":{"token":"check","pid":1}}`))
				}, allGoals)
			}()
			<-locked
			result := make(chan error, 1)
			go func() {
				if edit {
					holders := []string{lifecycleHolder}
					_, err := s.Edit("r", g.ID, "owner", "Changed", &holders, false)
					result <- err
				} else {
					_, err := s.Add("r", "owner", Input{Text: "New", Holders: []string{lifecycleHolder}})
					result <- err
				}
			}()
			select {
			case err := <-result:
				close(release)
				<-writer
				t.Fatalf("operation returned during transaction: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			close(release)
			if err := <-writer; err != nil {
				t.Fatal(err)
			}
			if err := <-result; err == nil || !strings.Contains(err.Error(), lifecycleHolder) {
				t.Fatalf("expected rejection after waiting, got %v", err)
			}
		})
	}
}

func TestLockedReadInvalidState(t *testing.T) {
	s := lifecycleStore(t)
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "state.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadLocked(); err == nil {
		t.Fatal("accepted invalid state")
	}
	if _, err := s.OpenHeldBy("r", "owner"); err == nil {
		t.Fatal("guard accepted invalid state")
	}
}

func TestCloseSessionChecksReplacementCandidates(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		available      bool
	}{
		{"open", `{"id":"` + lifecycleHolder + `"}`, true},
		{"closing", `{"id":"` + lifecycleHolder + `","closing":{"token":"clear","pid":1}}`, false},
		{"closed", `{"id":"` + lifecycleHolder + `","closed":"2026-10-17T12:00:00Z"}`, false},
		{"missing", "", false},
		{"invalid", `{`, false},
		{"mismatched", `{"id":"another"}`, false},
	} {
		for _, replaceSetter := range []bool{false, true} {
			role := "holder"
			if replaceSetter {
				role = "setter-and-holder"
			}
			t.Run(tc.name+"/"+role, func(t *testing.T) {
				s := lifecycleStore(t)
				writeHolder(t, s, `{"id":"`+lifecycleHolder+`"}`)
				other := "ses_20261017-120000-fedcba02"
				recordtest.WriteRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), other, []byte(`{"id":"`+other+`"}`))
				setter, parent := lifecycleHolder, ""
				if replaceSetter {
					setter, parent = other, lifecycleHolder
				}
				g, err := s.Add("r", setter, Input{Text: "unfinished", Holders: []string{other}})
				if err != nil {
					t.Fatal(err)
				}
				if tc.metadata == "" {
					recordtest.Remove(t, filepath.Join(filepath.Dir(s.Root), "sessions"), lifecycleHolder)
				} else {
					writeHolder(t, s, tc.metadata)
				}
				if err := s.CloseSession("r", other, parent, map[string]bool{lifecycleHolder: true}, ""); err != nil {
					t.Fatal(err)
				}
				got, err := s.Get("r", g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if replaceSetter {
					if got.State != "dropped" || got.Shared || got.Done == nil {
						t.Fatalf("own goal survived holder close: %+v", got)
					}
					return
				}
				if tc.available {
					if got.Setter != lifecycleHolder || !reflect.DeepEqual(got.Holders, []string{lifecycleHolder}) || got.Shared {
						t.Fatalf("open candidate was not selected: %+v", got)
					}
				} else {
					if len(got.Holders) != 0 || got.Shared {
						t.Fatalf("unavailable candidate became a holder: %+v", got)
					}
				}
			})
		}
	}
}

func TestGoalPublicationClearsDormancy(t *testing.T) {
	for _, action := range []string{"add holder", "add setter", "edit existing holder", "reopen goal"} {
		t.Run(action, func(t *testing.T) {
			s := lifecycleStore(t)
			body := `{"id":"` + lifecycleHolder + `","status":"dormant","finished":"2026-10-17T12:00:00Z","room":"keep","turns":7}`
			writeHolder(t, s, body)
			var err error
			if action == "add setter" {
				_, err = s.Add("r", lifecycleHolder, Input{Text: "New goal", Holders: []string{"owner"}})
			} else {
				g, addErr := s.Add("r", "owner", Input{Text: "New goal", Holders: []string{lifecycleHolder}})
				if addErr != nil {
					t.Fatal(addErr)
				}
				if action == "reopen goal" {
					if _, err = s.Finish("r", g.ID, "owner", "done", false); err != nil {
						t.Fatal(err)
					}
				}
				if action != "add holder" {
					writeHolder(t, s, body)
					_, err = s.Edit("r", g.ID, "owner", "Updated", nil, false)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := recordtest.ReadRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), lifecycleHolder)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Status   string
				Finished time.Time
				Room     string
				Turns    int
			}
			if err = json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != "idle" || !got.Finished.IsZero() || got.Room != "keep" || got.Turns != 7 {
				t.Fatalf("goal owner metadata = %s", raw)
			}
		})
	}
}

func TestHistoricalGoalHandoverPreservesDormancy(t *testing.T) {
	for _, state := range []string{"active", "done", "dropped"} {
		t.Run(state, func(t *testing.T) {
			s := lifecycleStore(t)
			g, err := s.Add("r", "child", Input{Text: "Child task", Holders: []string{"child"}})
			if err != nil {
				t.Fatal(err)
			}
			if state != "active" {
				if _, err = s.Finish("r", g.ID, "child", "Finished task", state == "dropped"); err != nil {
					t.Fatal(err)
				}
			}
			body := `{"id":"` + lifecycleHolder + `","status":"dormant","finished":"2026-10-17T12:00:00Z"}`
			writeHolder(t, s, body)
			if err = s.CloseSession("r", "child", lifecycleHolder, map[string]bool{lifecycleHolder: true}, ""); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get("r", g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state == "active" {
				if got.State != "dropped" || got.Done == nil {
					t.Fatalf("own active goal was not dropped: %+v", got)
				}
			} else if got.State != state || got.Setter != lifecycleHolder || !reflect.DeepEqual(got.Holders, []string{lifecycleHolder}) {
				t.Fatalf("historical handover = %+v", got)
			}
			raw, err := recordtest.ReadRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), lifecycleHolder)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != body {
				t.Fatalf("closing or historical handover changed dormant parent: %s", raw)
			}
		})
	}
}

// Goals live in dyna.db, and so do session records. Giving a dormant session
// a goal wakes its record on the goal store's own transaction; a second
// write transaction there would wait for itself.
func TestGoalForADormantHolderWakesItWithoutDeadlock(t *testing.T) {
	s := lifecycleStore(t)
	sessions := filepath.Join(filepath.Dir(s.Root), "sessions")
	recordtest.Write(t, sessions, lifecycleHolder, map[string]any{"id": lifecycleHolder, "status": "dormant", "finished": "2026-10-17T10:00:00Z"})
	added := make(chan error, 1)
	go func() {
		_, err := s.Add("r", "owner", Input{Text: "Wake up", Holders: []string{lifecycleHolder}})
		added <- err
	}()
	select {
	case err := <-added:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("adding a goal for a dormant holder deadlocked")
	}
	var rec struct{ Status string }
	recordtest.Read(t, sessions, lifecycleHolder, &rec)
	if rec.Status != "idle" {
		t.Fatalf("holder status = %q, want idle", rec.Status)
	}
}

// A goal can have many session holders; each needs its lock before the goal
// transaction, and none of that limits how many holders a goal has.
func TestGoalWithManySessionHolders(t *testing.T) {
	s := lifecycleStore(t)
	sessions := filepath.Join(filepath.Dir(s.Root), "sessions")
	var holders []string
	for i := range 8 {
		id := fmt.Sprintf("ses_20261017-120000-abc12%d", i)
		recordtest.Write(t, sessions, id, map[string]any{"id": id, "status": "dormant", "finished": "2026-10-17T10:00:00Z"})
		holders = append(holders, id)
	}
	g, err := s.Add("r", "owner", Input{Text: "Many hands", Holders: holders[:4]})
	if err != nil {
		t.Fatalf("add with four session holders: %v", err)
	}
	if _, err := s.Edit("r", g.ID, "owner", "", &holders, false); err != nil {
		t.Fatalf("edit to eight session holders: %v", err)
	}
	if _, err := s.Add("r", "owner", Input{Text: "Eight at once", Holders: holders}); err != nil {
		t.Fatalf("add with eight session holders: %v", err)
	}
	for _, id := range holders {
		var rec struct{ Status string }
		recordtest.Read(t, sessions, id, &rec)
		if rec.Status != "idle" {
			t.Fatalf("holder %s status = %q, want idle", id, rec.Status)
		}
	}
}
