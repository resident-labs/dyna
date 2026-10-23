package goal

import (
	"strings"

	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalservice"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestGoalCommandsUseShortOutputAndExplicitJSON(t *testing.T) {
	storetest.Isolate(t)
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(root, "sessions")}
	mgr.Rooms = rooms
	s := goalservice.New()
	s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
	s.Notify = notify.New(rooms, mgr)
	actor := authority.Actor{Role: authority.Owner}
	serveMutations(t, s, &actor)

	for _, tc := range []struct {
		name      string
		args      []string
		id, state string
	}{
		{"create", []string{"Ship another task"}, "g2", "active"},
		{"show", []string{"show", "g1"}, "g1", "active"},
		{"edit", []string{"edit", "g1", "Ship the revised task"}, "g1", "active"},
		{"done", []string{"done", "g1", "Shipped"}, "g1", "done"},
		{"drop", []string{"drop", "g1", "No longer needed"}, "g1", "dropped"},
	} {
		for _, asJSON := range []bool{false, true} {
			name := tc.name + "/human"
			if asJSON {
				name = tc.name + "/json"
			}
			t.Run(name, func(t *testing.T) {
				// Each case starts with its own goal IDs and history, while
				// the real engine and its store migrations are shared.
				rec, err := rooms.Create(room.CreateInput{Name: "work-" + strings.ReplaceAll(name, "/", "-")})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Store.Add(rec.ID, "owner", goals.Input{Text: "Ship the task", Shared: true}); err != nil {
					t.Fatal(err)
				}
				cmd := Command(Options{Service: func() *goalservice.Service { return s }})
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SilenceUsage = true
				args := append(append([]string{}, tc.args...), "--room", rec.Name)
				if asJSON {
					args = append(args, "--json")
				}
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatalf("command: %v; %s", err, out.String())
				}
				if !asJSON {
					if tc.name == "show" {
						// show carries the goal's mechanical status line.
						if prefix := tc.id + " " + tc.state + " · "; !strings.HasPrefix(out.String(), prefix) {
							t.Fatalf("human output = %q, want prefix %q", out.String(), prefix)
						}
					} else if want := tc.id + " " + tc.state + "\n"; out.String() != want {
						t.Fatalf("human output = %q, want %q", out.String(), want)
					}
					return
				}
				var got goals.Goal
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != tc.id || got.State != tc.state || got.Text == "" {
					t.Fatalf("JSON output = %q, %v", out.String(), err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				if _, ok := fields["edits"]; !ok {
					t.Fatal("JSON dropped full goal details")
				}
			})
		}
	}
}
