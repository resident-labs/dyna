package rules

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
		{"add", "AddRule", []string{"add", "Use the engine"}, 3},
		{"edit", "EditRule", []string{"edit", "r1", "Use the engine"}, 4},
		{"remove", "RemoveRule", []string{"rm", "r1"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			enginetest.Serve(t, t.TempDir(), map[string]websvc.Call{tc.method: func(_ context.Context, args []json.RawMessage) (any, error) {
				called = true
				if len(args) != tc.count || string(args[0]) != `"work"` {
					t.Errorf("arguments = %s", args)
				}
				return goals.Rule{ID: "r1"}, nil
			}})
			cmd := Command(Options{Service: func() *goalkeeper.Service { t.Fatal("write opened a local goal store"); return nil }})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append(tc.args, "--room", "work"))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !called || !strings.Contains(out.String(), "r1") {
				t.Fatalf("engine called = %v, output = %q", called, out.String())
			}
		})
	}
}

func TestWritesWithoutEngineFailWithCause(t *testing.T) {
	client := engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	for _, args := range [][]string{
		{"add", "Use the engine"},
		{"edit", "r1", "Use the engine"},
		{"rm", "r1"},
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
