package goal

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/goalkeeper"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/websvc"
)

func TestWritesCallEngine(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		args         []string
		count        int
	}{
		{"add", "AddGoal", []string{"Ship through engine", "--for", "@child"}, 3},
		{"edit", "EditGoalSelectors", []string{"edit", "g1", "New text", "--for", "@child"}, 4},
		{"done", "FinishGoal", []string{"done", "g1", "Shipped"}, 4},
		{"drop", "FinishGoal", []string{"drop", "g1"}, 4},
		{"wait", "WaitGoal", []string{"wait", "g1", "--until", "30m"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			enginetest.Serve(t, t.TempDir(), map[string]websvc.Call{tc.method: func(_ context.Context, args []json.RawMessage) (any, error) {
				called = true
				if len(args) != tc.count || string(args[0]) != `"work"` {
					t.Errorf("arguments = %s", args)
				}
				return goals.Goal{ID: "g1"}, nil
			}})
			cmd := Command(Options{Service: func() *goalkeeper.Service { t.Fatal("write opened a local goal store"); return nil }})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append(tc.args, "--room", "work"))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !called || !strings.Contains(out.String(), "g1") {
				t.Fatalf("engine called = %v, output = %q", called, out.String())
			}
		})
	}
}

func TestWritesWithoutEngineFailWithCause(t *testing.T) {
	client := engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	for _, args := range [][]string{
		{"Ship through engine", "--for", "@child"},
		{"edit", "g1", "New text", "--for", "@child"},
		{"done", "g1", "Shipped"},
		{"drop", "g1"},
		{"wait", "g1", "--until", "30m"},
	} {
		cmd := Command(Options{Client: client, Service: func() *goalkeeper.Service { t.Fatal("write opened a local goal store"); return nil }})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append(args, "--room", "work"))
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestMoveItemCallsEngineAndPrintsReferences(t *testing.T) {
	for _, toItem := range []string{"i4", ""} {
		moved := goals.ItemMove{From: goals.WorkRef{Goal: "g1", Item: "i2"}, To: goals.WorkRef{Goal: "g3", Item: toItem}}
		to := "g3"
		if toItem == "" {
			to = "new"
		}
		enginetest.Serve(t, t.TempDir(), map[string]websvc.Call{"MoveGoalItem": func(_ context.Context, args []json.RawMessage) (any, error) {
			if len(args) != 4 || string(args[0]) != `"work"` || string(args[1]) != `"g1"` || string(args[2]) != `"i2"` || string(args[3]) != `"`+to+`"` {
				t.Errorf("move arguments = %s", args)
			}
			return moved, nil
		}})
		for _, asJSON := range []bool{false, true} {
			cmd := Command(Options{Service: func() *goalkeeper.Service { t.Fatal("move opened a local goal store"); return nil }})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			args := []string{"item", "move", "g1", "i2", "--to", to, "--room", "work"}
			if asJSON {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if asJSON {
				var got goals.ItemMove
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got != moved {
					t.Fatalf("move JSON = %q, %v", out.String(), err)
				}
			} else if want := "moved g1/i2 to " + moved.To.String() + "\n"; out.String() != want {
				t.Fatalf("move output = %q; want %q", out.String(), want)
			}
		}
	}
}
