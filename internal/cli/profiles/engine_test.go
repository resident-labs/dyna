package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workeroutput"
)

func serveProfileEngine(t *testing.T, actor authority.Actor) (string, *atomic.Int32) {
	t.Helper()
	svc := &configsvc.Service{}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "UpsertProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: svc.UpsertProfile},
		{Name: "SetProfileUses", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: svc.SetProfileUses},
		{Name: "SeedProfiles", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: svc.SeedProfiles},
		{Name: "RemoveProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.RemoveProfile},
		{Name: "SetDefaultProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.SetDefaultProfile},
		{Name: "SetProfileDisabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.SetProfileDisabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Int32
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			called.Add(1)
			// Only the fake engine chooses the caller, never the CLI stamp or context.
			return call(authority.WithActor(ctx, actor), args)
		}
	}
	return enginetest.Serve(t, store.DataDir(), calls), &called
}

func isolateProfiles(t *testing.T) {
	t.Helper()
	storetest.Isolate(t)
	serveProfileEngine(t, authority.Actor{Role: authority.Owner})
}

func profileMutationArgs() [][]string {
	return [][]string{
		{"add", "--name", "new", "--harness", "mock"},
		{"set", "subject", "--use", "agents"},
		{"remove", "subject"}, {"set-default", "subject"},
		{"enable", "subject"}, {"disable", "subject"},
		{"init-codex"}, {"init-claude"}, {"init-antigravity"}, {"init-muse"},
	}
}

func seedProfileRegistry(t *testing.T) []byte {
	t.Helper()
	reg := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{Name: "subject", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(reg.Path)
	if err != nil {
		t.Fatal(err)
	}
	return before
}

func TestProfileMutationEngineFailureHasNoLocalFallback(t *testing.T) {
	storetest.Isolate(t)
	socket, called := serveProfileEngine(t, authority.Actor{Role: authority.Owner})
	previous := engineClient
	t.Cleanup(func() { engineClient = previous })
	engineClient = func() *engineclient.Client {
		return engineclient.New(socket+".missing", engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	for _, args := range profileMutationArgs() {
		t.Run(args[0], func(t *testing.T) {
			before := seedProfileRegistry(t)
			cmd := NewCommand()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, engineclient.ErrNotRunning) {
				t.Errorf("engine failure cause = %v", err)
			}
			after, err := os.ReadFile(profile.DefaultPath())
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("engine failure changed profiles: %v", err)
			}
		})
	}
	if called.Load() != 0 {
		t.Fatalf("unexpected engine calls = %d", called.Load())
	}
}

func TestProfileAddSendsFullFieldsToEngine(t *testing.T) {
	storetest.Isolate(t)
	before := seedProfileRegistry(t)
	want := profile.Profile{
		Name: "complete", Harness: profile.HarnessCustom, RuntimeVersion: "2.3.4",
		Description: "keep spacing ", Model: "first", Models: []string{"first", "second"},
		Taste: 8, Intelligence: 7, Cost: 6, ExtraArgs: []string{"--flag", " spaced "},
		Command:    []string{"fixture", "{{prompt}}"},
		Output:     &workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL},
		TimeoutSec: 2100, Default: true, SafeMode: true,
		Uses: []string{profile.UseAgents}, MaxConcurrent: 3, MaxCallsPerRun: 4,
	}
	var called atomic.Int32
	calls, err := websvc.Bind([]websvc.Binding{{Name: "UpsertProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
		Method: func(p profile.Profile) error {
			called.Add(1)
			if !reflect.DeepEqual(p, want) {
				t.Errorf("profile request = %#v, want %#v", p, want)
			}
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	out := runProfilesCommand(t, "add", "--name", want.Name, "--harness", want.Harness,
		"--runtime-version", want.RuntimeVersion, "--desc", want.Description,
		"--model", "ignored", "--models", " first ,second,first",
		"--taste", "8", "--intelligence", "7", "--cost", "6",
		"--extra-arg=--flag", "--extra-arg", " spaced ",
		"--command", "fixture", "--command", "{{prompt}}",
		"--output-dialect", workeroutput.DialectCodexJSONL, "--timeout", "2100",
		"--default", "--safe-mode", "--use", "agents",
		"--limit-concurrent", "3", "--limit-calls", "4")
	if called.Load() != 1 || out != "saved profile \"complete\" (custom)\n" {
		t.Errorf("engine calls = %d, output = %q", called.Load(), out)
	}
	after, err := os.ReadFile(profile.DefaultPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("CLI bypassed the engine: %v", err)
	}
}

func TestProfileCommandsRenderEngineResults(t *testing.T) {
	storetest.Isolate(t)
	before := seedProfileRegistry(t)
	var called atomic.Int32
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "SetProfileUses", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
			Method: func(name string, uses []string) ([]string, error) {
				called.Add(1)
				if name != "subject" || !reflect.DeepEqual(uses, []string{profile.UseAgents}) {
					t.Errorf("use request = %q, %v", name, uses)
				}
				return []string{profile.UseServices}, nil
			}},
		{Name: "SeedProfiles", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
			Method: func(harness string, defaults []profile.Profile) ([]configsvc.SeededProfile, error) {
				called.Add(1)
				var bundle struct {
					Profiles []profile.Profile `json:"profiles"`
				}
				if err := json.Unmarshal(defaultProfilesJSON, &bundle); err != nil || !reflect.DeepEqual(defaults, bundle.Profiles) {
					t.Errorf("seed request lost bundle fields: %v", err)
				}
				return []configsvc.SeededProfile{
					{Name: "kept", Kept: true},
					{Name: "remote", Harness: harness, Model: "engine-model"},
				}, nil
			}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	if out := runProfilesCommand(t, "set", "subject", "--use", "agents"); out != "profile \"subject\" uses: services\n" {
		t.Errorf("uses output = %q", out)
	}
	for command, harness := range map[string]string{
		"init-codex": profile.HarnessCodex, "init-claude": profile.HarnessClaudeCode,
		"init-antigravity": profile.HarnessAntigravity, "init-muse": profile.HarnessMuse,
	} {
		out := runProfilesCommand(t, command)
		want := "  - kept     exists, kept\n  ✓ remote   " + harness + " · engine-model\n" +
			"browse and edit them in the desktop app (Settings, Profiles); agents see them via `dyna profiles list --json`\n"
		if out != want {
			t.Errorf("%s output = %q, want %q", command, out, want)
		}
	}
	if called.Load() != 5 {
		t.Errorf("engine calls = %d, want 5", called.Load())
	}
	after, err := os.ReadFile(profile.DefaultPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("CLI bypassed engine results: %v", err)
	}
}
