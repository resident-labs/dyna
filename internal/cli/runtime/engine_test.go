package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func runHarness(t *testing.T, args ...string) error {
	t.Helper()
	c := NewCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(args)
	return c.ExecuteContext(t.Context())
}

// Removing an account is an engine call, so the registry commit publishes the
// account change from the engine process.
func TestAccountRemoveIsAnEngineCall(t *testing.T) {
	storetest.Isolate(t)
	type request struct{ args []json.RawMessage }
	requests := make(chan request, 1)
	enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{
		"RemovePrivateRuntimeAccount": func(_ context.Context, args []json.RawMessage) (any, error) {
			requests <- request{args}
			return nil, nil
		},
	})
	if err := runHarness(t, "accounts", "rm", "account-1", "--confirm"); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	if len(got.args) != 2 || string(got.args[0]) != `"account-1"` || string(got.args[1]) != "true" {
		t.Fatalf("engine got %s", got.args)
	}
}

// With no engine, account removal and the personal-login copy fail and write
// nothing locally.
func TestAccountWritesHaveNoLocalFallback(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	previous := engineClient
	t.Cleanup(func() { engineClient = previous })
	engineClient = func() *engineclient.Client {
		return engineclient.New("/nonexistent/engine.sock", engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	for _, args := range [][]string{
		{"accounts", "rm", "account-1", "--confirm"},
		{"login", "codex", "--use-personal"},
		{"accounts", "activate", "account-1"},
		{"config-import", "--account", "account-1", "--preview-hash", "abc"},
		{"gc"},
	} {
		if err := runHarness(t, args...); !errors.Is(err, engineclient.ErrNotRunning) {
			t.Errorf("%v: err = %v, want the engine not running", args, err)
		}
	}
}

// A command that changes an account or the runtime sends one engine call with
// its arguments, so the engine does the write and publishes the change.
func TestRuntimeWritesAreEngineCalls(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	type request struct {
		method string
		args   string
	}
	requests := make(chan request, 1)
	calls := map[string]websvc.Call{}
	for _, method := range []string{"ActivatePrivateRuntimeAccount", "ImportPrivateConfig", "GCPrivateRuntime"} {
		calls[method] = func(_ context.Context, args []json.RawMessage) (any, error) {
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			requests <- request{method, string(raw)}
			return nil, nil
		}
	}
	enginetest.Serve(t, store.DataDir(), calls)
	for _, tc := range []struct {
		args   []string
		method string
		want   string
	}{
		{[]string{"accounts", "activate", "account-1"}, "ActivatePrivateRuntimeAccount", `["account-1"]`},
		{[]string{"config-import", "--account", "account-1", "--preview-hash", "abc"}, "ImportPrivateConfig", `["account-1","abc"]`},
		{[]string{"gc"}, "GCPrivateRuntime", `[]`},
	} {
		if err := runHarness(t, tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got := <-requests; got.method != tc.method || got.args != tc.want {
			t.Fatalf("%v sent %+v, want %s %s", tc.args, got, tc.method, tc.want)
		}
	}
}

// The engine prepares the harness (installs its bundle) while it looks for the
// personal login, so that call must come before the copy.
func TestUsePersonalPreparesBeforeCopying(t *testing.T) {
	storetest.Isolate(t)
	order := make(chan string, 2)
	calls := map[string]websvc.Call{
		"FindPersonalLogin": func(_ context.Context, args []json.RawMessage) (any, error) {
			order <- "find " + string(args[0])
			return nil, nil
		},
		"UsePersonalLogin": func(_ context.Context, args []json.RawMessage) (any, error) {
			order <- "use " + string(args[0])
			return map[string]any{"accounts": []map[string]any{
				{"id": "other", "harness": "codex", "active": false},
				{"id": "copied", "harness": "codex", "active": true},
			}}, nil
		},
	}
	enginetest.Serve(t, store.DataDir(), calls)
	var out bytes.Buffer
	c := NewCommand()
	c.SetOut(&out)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"login", "codex", "--use-personal"})
	if err := c.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if first, second := <-order, <-order; first != `find "codex"` || second != `use "codex"` {
		t.Fatalf("calls were %s then %s", first, second)
	}
	if !strings.Contains(out.String(), `"id": "copied"`) {
		t.Fatalf("output %q", out.String())
	}
}
