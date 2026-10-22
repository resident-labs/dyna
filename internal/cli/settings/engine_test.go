package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// The fake engine supplies the verified actor independently of the CLI context.
func serveSetSetting(t *testing.T, actor authority.Actor, set func(context.Context, string, string) (configsvc.Item, error)) (string, *atomic.Int32) {
	t.Helper()
	var called atomic.Int32
	calls, err := websvc.Bind([]websvc.Binding{{Name: "SetSetting", Access: websvc.Write, Scope: websvc.ScopeAdmin,
		Method: func(ctx context.Context, key, value string) (configsvc.Item, error) {
			called.Add(1)
			return set(authority.WithActor(ctx, actor), key, value)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return enginetest.Serve(t, store.DataDir(), calls), &called
}

func TestSettingsSetUsesEngineResult(t *testing.T) {
	storetest.Isolate(t)
	baseline := settings.Defaults()
	if err := baseline.Save(); err != nil {
		t.Fatal(err)
	}
	_, called := serveSetSetting(t, authority.Actor{Role: authority.Owner}, func(ctx context.Context, key, value string) (configsvc.Item, error) {
		if authority.Of(ctx).Role != authority.Owner || key != settings.KeyMaxFileBytes || value != "30MiB" {
			t.Errorf("engine request = %q, %q, %+v", key, value, authority.Of(ctx))
		}
		return configsvc.Item{Key: key, Value: int64(7 << 20), Display: "7 MiB", Source: settings.SourceFile, LiveVsRestart: settings.LiveNextOperation, Kind: "bytes", Help: "Engine-only metadata"}, nil
	})
	for _, asJSON := range []bool{false, true} {
		cmd := NewCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		args := []string{"set", settings.KeyMaxFileBytes, "30MiB"}
		if asJSON {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !asJSON {
			if output.String() != "set content.maxFileBytes = 7 MiB\n" {
				t.Errorf("set output = %q", output.String())
			}
		} else {
			var item listItem
			if err := json.Unmarshal(output.Bytes(), &item); err != nil {
				t.Fatal(err)
			}
			if item.Key != settings.KeyMaxFileBytes || item.Value != float64(7<<20) || item.Display != "7 MiB" || item.Source != settings.SourceFile || item.LiveVsRestart != settings.LiveNextOperation {
				t.Errorf("set JSON = %+v", item)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(output.Bytes(), &fields); err != nil || len(fields) != 5 {
				t.Errorf("CLI JSON fields = %s, %v", output.Bytes(), err)
			}
		}
	}
	if called.Load() != 2 {
		t.Errorf("SetSetting calls = %d, want 2", called.Load())
	}
	saved, err := settings.Load(settings.DefaultPath())
	if err != nil || saved.MaxFileBytes != baseline.MaxFileBytes {
		t.Fatalf("CLI wrote locally: file limit = %d, %v", saved.MaxFileBytes, err)
	}
}

func TestSettingsSetWithoutEngineFailsWithCause(t *testing.T) {
	storetest.Isolate(t)
	baseline := settings.Defaults()
	if err := baseline.Save(); err != nil {
		t.Fatal(err)
	}
	socket, called := serveSetSetting(t, authority.Actor{Role: authority.Owner}, configsvc.Set)
	previous := engineClient
	t.Cleanup(func() { engineClient = previous })
	engineClient = func() *engineclient.Client {
		return engineclient.New(socket+".missing", engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	cmd := NewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"set", settings.KeySubagentMaxDepth, "4"})
	err := cmd.Execute()
	if !errors.Is(err, engineclient.ErrNotRunning) {
		t.Errorf("missing engine cause = %v", err)
	}
	if called.Load() != 0 {
		t.Fatalf("unexpected engine calls = %d", called.Load())
	}
	saved, err := settings.Load(settings.DefaultPath())
	if err != nil || saved.SubagentMaxDepth != baseline.SubagentMaxDepth {
		t.Fatalf("CLI used a local fallback: depth = %d, %v", saved.SubagentMaxDepth, err)
	}
}

func TestSettingsSetPreservesLargeJSONNumber(t *testing.T) {
	storetest.Isolate(t)
	serveSetSetting(t, authority.Actor{Role: authority.Owner}, configsvc.Set)
	const value = "1000000000000000128"
	cmd := NewCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"set", settings.KeyWorktreesMinFreeBytes, value, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var item struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(output.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if string(item.Value) != value {
		t.Fatalf("setting value rounded during transport: got %s, want %s", item.Value, value)
	}
	saved, err := settings.Load(settings.DefaultPath())
	if err != nil || saved.WorktreesMinFreeBytes != 1000000000000000128 {
		t.Fatalf("saved value = %d, %v", saved.WorktreesMinFreeBytes, err)
	}
}
