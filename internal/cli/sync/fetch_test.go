package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func TestFetchCallsEngineWithoutLocalStateWrites(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	calls := 0
	enginetest.Serve(t, t.TempDir(), map[string]websvc.Call{
		"SyncFetch": func(_ context.Context, args []json.RawMessage) (any, error) {
			calls++
			if len(args) != 0 {
				t.Fatalf("unexpected fetch arguments: %s", args)
			}
			return lanesync.Status{"/repo": {Remotes: map[string]lanesync.Remote{"origin": {Ahead: 2, Behind: 3}}}}, nil
		},
	})
	c := NewCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"fetch"})
	if err := c.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), "/repo origin: 2 ahead, 3 behind") {
		t.Fatalf("calls=%d, output=%q", calls, out.String())
	}
	for _, name := range []string{"sync.json", "dyna.db"} {
		if _, err := os.Stat(filepath.Join(store.DataDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("fetch wrote local %s: %v", name, err)
		}
	}
}

func TestFetchReturnsEngineWriteFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	enginetest.Serve(t, t.TempDir(), map[string]websvc.Call{
		"SyncFetch": func(context.Context, []json.RawMessage) (any, error) {
			return nil, errors.New("sync fetch refused by authority")
		},
	})
	c := NewCommand()
	c.SetArgs([]string{"fetch"})
	if err := c.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "sync fetch refused by authority") {
		t.Fatalf("write failure: %v", err)
	}
}
