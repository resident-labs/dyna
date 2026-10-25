package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workeroutput"
)

func TestCheckIncludesLimitedUseAndSkipsDisabled(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "solo", "--harness", "mock", "--use", "agents")
	runProfilesCommand(t, "add", "--name", "off", "--harness", "mock")
	runProfilesCommand(t, "disable", "off")
	out := runProfilesCommand(t, "check")
	for _, want := range []string{"solo", "harness: mock", "effective dialect:", "binary: installed", "Capabilities:"} {
		if !strings.Contains(out, want) {
			t.Errorf("check missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "off\n") || strings.Contains(out, "live:") {
		t.Fatalf("unexpected check output: %s", out)
	}
}

func TestCheckSelection(t *testing.T) {
	store := &profile.Store{Profiles: []profile.Profile{{Name: "one"}, {Name: "two", Uses: []string{profile.UseAgents}}, {Name: "off", Disabled: true}}}
	got, err := selectCheckProfiles(store, []string{"two", "two", "one"})
	if err != nil || len(got) != 2 || got[0].Name != "two" || got[1].Name != "one" {
		t.Fatalf("selection = %#v, %v", got, err)
	}
	for _, name := range []string{"off", "missing"} {
		if _, err := selectCheckProfiles(store, []string{name}); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestCheckReportsAccountReadiness(t *testing.T) {
	storetest.Isolate(t)
	// A managed profile needs Dyna's private copy and an active sign-in.
	reg := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{Name: "native", Harness: profile.HarnessCodex, Taste: 5, Intelligence: 5, Cost: 5}}}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	cmd := NewCommand()
	cmd.SetArgs([]string{"check", "native"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 profile check(s) failed") {
		t.Fatalf("error = %v, want a failed check for a profile that cannot start", err)
	}
	for _, want := range []string{"account: ", "next: dyna harness login codex"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("check output missing %q: %s", want, out.String())
		}
	}
}

func TestCheckBinaryLocatesWithoutStartingCommand(t *testing.T) {
	isolateProfiles(t)
	marker := filepath.Join(t.TempDir(), "started")
	bin := t.TempDir()
	fake := filepath.Join(bin, "pi")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Name: "fake", Harness: profile.HarnessPi, Env: map[string]string{"PATH": bin}}
	got, err := checkBinary(p)
	if err != nil || got != fake {
		t.Fatalf("checkBinary() = %q, %v; want the fake pi path", got, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("static check started the binary: %v", err)
	}
	missing := profile.Profile{Name: "missing", Harness: profile.HarnessPi, Env: map[string]string{"PATH": t.TempDir()}}
	if _, err := checkBinary(missing); err == nil {
		t.Fatal("checkBinary() resolved a binary that is not on PATH")
	}
}

func TestValidateProbeEvidence(t *testing.T) {
	message, _ := json.Marshal(workeroutput.Event{Kind: "message", Role: "assistant", Text: probeReply})
	tool, _ := json.Marshal(workeroutput.Event{Kind: "tool", Name: "shell", State: "completed"})
	events := append(append(message, '\n'), tool...)
	positive := harness.Result{Usage: workeroutput.Usage{InputTokens: 1}}
	for _, tc := range []struct {
		name, dialect string
		result        harness.Result
		events        []byte
		want          string
	}{
		{"complete", workeroutput.DialectCodexJSONL, positive, events, ""},
		{"no text", workeroutput.DialectCodexJSONL, positive, tool, "text events"},
		{"no tools", workeroutput.DialectCodexJSONL, positive, message, "tool events"},
		{"no usage", workeroutput.DialectCodexJSONL, harness.Result{}, events, "token usage"},
		{"muse usage unavailable", workeroutput.DialectMuseJSONL, harness.Result{}, events, ""},
		{"plain text", workeroutput.DialectText, harness.Result{}, message, ""},
		{"raw is not text", workeroutput.DialectCodexJSONL, positive, []byte(`{"kind":"raw","raw":"DYNA_PROFILE_CHECK_OK"}`), "text events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProbe(tc.dialect, tc.result, tc.events)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestListAndAddUseEffectiveDialect(t *testing.T) {
	isolateProfiles(t)
	runProfilesCommand(t, "add", "--name", "native", "--harness", "codex")
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.Get("native")
	if p.Output != nil {
		t.Fatalf("add persisted default override: %#v", p.Output)
	}
	if !strings.Contains(runProfilesCommand(t, "list"), "effective dialect: codex-jsonl") {
		t.Fatal("human list omitted effective dialect")
	}
	var rows []profileListRow
	if err := json.Unmarshal([]byte(runProfilesCommand(t, "list", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EffectiveDialect != workeroutput.DialectCodexJSONL || rows[0].Output != nil {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestCheckNativeBinaryUsesProfileEnvironment(t *testing.T) {
	dir := t.TempDir()
	for harnessName, binary := range map[string]string{
		profile.HarnessClaudeCode: "claude", profile.HarnessCodex: "codex",
		profile.HarnessOpenCode: "opencode", profile.HarnessMuse: "muse",
	} {
		path := filepath.Join(dir, binary)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
		got, err := checkBinary(profile.Profile{Harness: harnessName, Env: map[string]string{"PATH": dir}})
		if err != nil || got != path {
			t.Errorf("%s binary = %q, %v; want %s", harnessName, got, err, path)
		}
	}
}

func TestProbeCannotPassFromUserEcho(t *testing.T) {
	user, _ := json.Marshal(workeroutput.Event{Kind: "message", Role: "user", Text: probeReply})
	tool, _ := json.Marshal(workeroutput.Event{Kind: "tool", Name: "shell", State: "completed"})
	events := append(append(user, '\n'), tool...)
	if err := validateProbe(workeroutput.DialectCodexJSONL, harness.Result{Usage: workeroutput.Usage{InputTokens: 1}}, events); err == nil {
		t.Fatal("user prompt echo passed as assistant reply")
	}
}

func TestLiveCheckUsesEngine(t *testing.T) {
	for _, tc := range []struct {
		name, probeError, callError string
	}{
		{name: "passes"},
		{name: "probe fails", probeError: "tool events missing"},
		{name: "engine refuses", callError: "profile check refused by authority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Isolate(t)
			reg := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{Name: "probe", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
			if err := reg.Save(); err != nil {
				t.Fatal(err)
			}
			var called atomic.Int32
			calls, err := websvc.Bind([]websvc.Binding{{
				Name: "ConfigCheckProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin,
				Method: func(ctx context.Context, name string, live bool) (configsvc.ProfileCheck, error) {
					called.Add(1)
					caller, identified := websvc.CallerFromContext(ctx)
					if name != "probe" || !live || !identified || caller.Kind != websvc.Owner {
						t.Errorf("probe request = %q, %t, caller=%+v", name, live, caller)
					}
					if tc.callError != "" {
						return configsvc.ProfileCheck{}, errors.New(tc.callError)
					}
					return configsvc.ProfileCheck{Name: name, ProbeError: tc.probeError}, nil
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			enginetest.Serve(t, store.DataDir(), calls)
			// A static check must not start a probe in either process.
			runProfilesCommand(t, "check", "probe")
			if called.Load() != 0 {
				t.Fatal("static check called the probe")
			}
			cmd := NewCommand()
			cmd.SetArgs([]string{"check", "probe", "--live"})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			err = cmd.ExecuteContext(t.Context())
			wantError := tc.probeError + tc.callError
			if called.Load() != 1 {
				t.Fatalf("engine calls = %d; want 1; output: %s", called.Load(), out.String())
			}
			if wantError == "" {
				if err != nil || !strings.Contains(out.String(), "live: PASS") {
					t.Fatalf("live check = %v; output: %s", err, out.String())
				}
			} else if err == nil || !strings.Contains(out.String(), wantError) || strings.Contains(out.String(), "live: PASS") {
				t.Fatalf("live check = %v; output: %s; want failure with %q", err, out.String(), wantError)
			}
		})
	}
}
