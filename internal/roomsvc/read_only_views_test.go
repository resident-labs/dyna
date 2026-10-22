package roomsvc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestSessionViewsNeverRepairOrphans(t *testing.T) {
	reads := []struct {
		name string
		read func(*Service, string) (string, error)
	}{
		{"profile", func(s *Service, ref string) (string, error) {
			got, err := s.Profile(ref, 2)
			return got.Member.ID, err
		}},
		{"transcript", func(s *Service, ref string) (string, error) {
			got, err := s.Transcript(ref)
			return got.Member.ID, err
		}},
		{"transcript turn", func(s *Service, ref string) (string, error) {
			got, err := s.TranscriptTurn(ref, 1)
			return got.Member.ID, err
		}},
	}
	for _, read := range reads {
		for _, selector := range []string{"id", "address", "name"} {
			t.Run(read.name+"/"+selector, func(t *testing.T) {
				e := newEnv(t)
				rec := e.room("alpha")
				parent := subagent.Meta{ID: "ses_20261017-120000-00000001", Name: "lead", RoomID: rec.ID, Room: rec.Name, Profile: "mock", Turns: 1, Depth: 1}
				child := subagent.Meta{ID: "ses_20261017-120000-00000002", Name: "orphan", RoomID: rec.ID, Room: rec.Name, Profile: "mock", Turns: 1, Depth: 2, ParentSession: parent.ID}
				before := map[string][]byte{}
				save := func(path string, value any) {
					t.Helper()
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
					before[path] = data
				}
				for _, meta := range []subagent.Meta{parent, child} {
					dir, err := e.mgr.Store.Dir(meta.ID)
					if err != nil {
						t.Fatal(err)
					}
					recordtest.Write(t, e.mgr.Store.Root, meta.ID, meta)
					save(filepath.Join(dir, "turns", "1.json"), subagent.Turn{N: 1, Status: subagent.TurnRunning, Prompt: "unfinished task"})
				}
				log := filepath.Join(e.svc.Notify.Rooms.Root, rec.ID, "messages.jsonl")
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				before[log] = data
				ref := child.ID
				switch selector {
				case "address":
					ref = child.Address()
				case "name":
					ref = child.Name
				}
				got, err := read.read(e.svc, ref)
				if err != nil || got != child.ID {
					t.Fatalf("read %q = %q, %v", ref, got, err)
				}
				for path, want := range before {
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, want) {
						t.Errorf("read changed %s: %q, %v; want %q", path, got, err, want)
					}
				}
			})
		}
	}
}
