package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

func TestCallKeyIncludesCwdAndExplicitLabel(t *testing.T) {
	key := testCallKey("worker", "task", "schema", "/jobs/a", "a")
	for _, other := range []string{
		testCallKey("worker", "task", "schema", "/jobs/b", "a"),
		testCallKey("worker", "task", "schema", "/jobs/a", "b"),
		testCallKey("worker", "task", "schema", "/jobs/a", ""),
		testCallKey("other", "task", "schema", "/jobs/a", "a"),
		testCallKey("worker", "other", "schema", "/jobs/a", "a"),
		testCallKey("worker", "task", "other", "/jobs/a", "a"),
	} {
		if key == other {
			t.Fatal("distinct call identities collided")
		}
	}
	// JSON tuple framing also avoids ambiguities from embedded NULs.
	if testCallKey("a\x00b", "c", "", "", "") == testCallKey("a", "b\x00c", "", "", "") {
		t.Fatal("tuple framing is ambiguous")
	}
}

func profileCallKey(p profile.Profile, prompt, schema, cwd, label string) string {
	return callKey(p, prompt, schema, cwd, label, resolveAgentTimeout(defaultAgentTimeout, false, p))
}

func testCallKey(name, prompt, schema, cwd, label string) string {
	return profileCallKey(profile.Profile{Name: name}, prompt, schema, cwd, label)
}

// legacyCallKey creates historical journal fixtures, never execution keys.
func legacyCallKey(name, prompt, schema string) string {
	h := sha256.Sum256([]byte(name + "\x00" + prompt + "\x00" + schema))
	return hex.EncodeToString(h[:16])
}

func TestCacheRejectsOldKeysAndAmbiguousIdentities(t *testing.T) {
	current := testCallKey("worker", "task", "", "/jobs/a", "same")
	for _, tc := range []struct {
		name    string
		entries []runstore.JournalEntry
	}{
		{"legacy", []runstore.JournalEntry{{Key: legacyCallKey("worker", "task", ""), Label: "same", Result: "stale"}}},
		{"v2", []runstore.JournalEntry{{Key: "v2:old", Label: "same", Result: "stale"}}},
		{"repeated identity", []runstore.JournalEntry{{Key: current, Result: "first"}, {Key: current, Result: "second"}}},
		{"error hole", []runstore.JournalEntry{{Key: current, Result: "first"}, {Key: current, Error: "canceled"}}},
		{"removed worktree", []runstore.JournalEntry{{Key: current, Result: "isolated", Worktree: "removed"}}},
		{"isolated dir", []runstore.JournalEntry{{Key: current, Result: "isolated", Dir: "removed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewCache(tc.entries)
			for _, key := range []string{current, tc.entries[0].Key} {
				if _, ok := cache.pop(key); ok {
					t.Fatal("unproven, ambiguous or isolated result replayed")
				}
			}
			if cache.Stats().Hits != 0 {
				t.Fatal(cache.Stats())
			}
		})
	}
}

func TestCurrentCacheDoesNotFallbackWhenCwdOrLabelChanges(t *testing.T) {
	key := testCallKey("worker", "task", "", "/jobs/a", "job")
	cache := NewCache([]runstore.JournalEntry{
		{Key: key, Label: "job", Result: "a"},
		{Key: legacyCallKey("worker", "task", ""), Result: "stale"},
		{Key: "v2:old", Result: "stale"},
	})
	for _, changed := range []string{testCallKey("worker", "task", "", "/jobs/b", "job"), testCallKey("worker", "task", "", "/jobs/a", "renamed")} {
		if _, ok := cache.pop(changed); ok {
			t.Fatal("changed cwd or label replayed")
		}
	}
	if result, ok := cache.pop(key); !ok || result != "a" {
		t.Fatalf("original result = %v, %t", result, ok)
	}
	if _, ok := cache.pop(key); ok {
		t.Fatal("result reused twice")
	}
}

func TestExecuteResumeReverseCompletionOrderWithCanceledHole(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv(runstore.AgentJournalRootEnv, "")
			base := t.TempDir()
			gate := filepath.Join(base, "finished-fast")
			dirs := map[string]string{}
			for _, name := range []string{"slow", "fast", "hole"} {
				dirs[name] = filepath.Join(base, name)
				if err := os.Mkdir(dirs[name], 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dirs[name], "answer"), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := &profile.Store{Profiles: []profile.Profile{{Name: "local", Harness: profile.HarnessCustom, Default: true,
				Command: []string{"/bin/sh", "-c", `answer=$(cat answer); if [ "$answer" = slow ]; then while [ ! -f "$GATE" ]; do sleep 0.01; done; fi; printf '%s' "$answer"`},
				Env:     map[string]string{"GATE": gate}, Taste: 5, Intelligence: 5, Cost: 5}}}
			script := `return await parallel(args.jobs.map(job => () => agent("identical task", {profile:"local",label:job.label,cwd:job.cwd})));`
			jobs := func(names ...string) any {
				entries := []map[string]string{}
				for _, name := range names {
					entries = append(entries, map[string]string{"label": name, "cwd": dirs[name]})
				}
				return map[string]any{"jobs": entries}
			}
			t.Setenv("DYNA_RUN_ID", "wf_cache-source")
			source, err := runstore.Create("cache source", script, jobs("slow", "fast"))
			if err != nil {
				t.Fatal(err)
			}
			// Gates and events end each wait. This only bounds a real hang, and
			// the three runs share it, so a loaded host must not trip it.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := Execute(ctx, Options{ScriptSrc: script, Args: jobs("slow", "fast"), Store: store, Run: source, WorkDir: base, OnEvent: func(event runstore.Event) {
				if event.T == "agent_end" && event.Label == "fast" && event.Status == "ok" {
					if err := os.WriteFile(gate, nil, 0600); err != nil {
						t.Error(err)
					}
				}
			}})
			source.Finish("ok", result, err)
			if err != nil || result != `["slow","fast"]` {
				t.Fatalf("source = %s, %v", result, err)
			}
			entries, err := runstore.ReadJournal(source.Meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[0].ID != 2 || entries[1].ID != 1 {
				t.Fatalf("completion order = %#v", entries)
			}
			for i := range entries {
				if legacy {
					entries[i].Key = legacyCallKey("local", "identical task", "")
					entries[i].Cwd = ""
				}
			}
			holeKey := profileCallKey(store.Profiles[0], "identical task", "", dirs["hole"], "hole")
			if legacy {
				holeKey = legacyCallKey("local", "identical task", "")
			}
			entries = append(entries, runstore.JournalEntry{ID: 3, Label: "hole", Key: holeKey, Error: "context canceled"})
			cache := NewCache(entries)
			t.Setenv("DYNA_RUN_ID", "wf_cache-resumed")
			resumed, err := runstore.Create("cache resumed", script, jobs("fast", "hole", "slow"))
			if err != nil {
				t.Fatal(err)
			}
			result, err = Execute(ctx, Options{ScriptSrc: script, Args: jobs("fast", "hole", "slow"), Store: store, Run: resumed, WorkDir: base, Cache: cache})
			resumed.Finish("ok", result, err)
			if err != nil || result != `["fast","hole","slow"]` {
				t.Fatalf("resumed = %s, %v", result, err)
			}
			wantHits := 2
			if legacy {
				wantHits = 0
			}
			if cache.Stats() != (CacheStats{Hits: wantHits, PriorCalls: 3}) {
				t.Fatal(cache.Stats())
			}
			output, err := runstore.ReadJournal(resumed.Meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(output) != 3 {
				t.Fatalf("resumed entries = %d", len(output))
			}
			for _, entry := range output {
				if entry.Cached != (!legacy && entry.Label != "hole") || entry.Cwd != dirs[entry.Label] || entry.Key != profileCallKey(store.Profiles[0], "identical task", "", entry.Cwd, entry.Label) {
					t.Fatalf("incorrect replay metadata: %#v", entry)
				}
			}
			// A second resume must use the upgraded identities, including the retried hole.
			again := NewCache(output)
			result, err = Execute(ctx, Options{ScriptSrc: script, Args: jobs("hole", "slow", "fast"), Store: store, WorkDir: base, Cache: again})
			var decoded []string
			if err != nil || json.Unmarshal([]byte(result), &decoded) != nil || result != `["hole","slow","fast"]` || again.Stats().Hits != 3 {
				t.Fatalf("second resume = %s, %v, %#v", result, err, again.Stats())
			}
		})
	}
}

func TestExecuteCacheSameLabelDifferentCwdAndGeneratedLabels(t *testing.T) {
	for _, label := range []string{"same job label", ""} {
		t.Run(fmt.Sprintf("label=%q", label), func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv(runstore.AgentJournalRootEnv, "")
			base := t.TempDir()
			dirs := []string{}
			for _, name := range []string{"a", "b", "changed"} {
				dir := filepath.Join(base, name)
				dirs = append(dirs, dir)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Cache identity does not need the mock's simulated work delay.
			const prompt = "SLEEP:0\nRESPOND: executed"
			script := fmt.Sprintf(`const results=[]; for(const cwd of args.dirs) results.push(await agent(%q,{profile:"local",label:args.label,cwd})); return results;`, prompt)
			store := &profile.Store{Profiles: []profile.Profile{{Name: "local", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5}}}
			t.Setenv("DYNA_RUN_ID", "wf_cwd-cache-source")
			source, err := runstore.Create("cwd source", script, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Execute waits for completion; runner load must not cancel either run.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result, err := Execute(ctx, Options{ScriptSrc: script, Store: store, Run: source, WorkDir: base, Args: map[string]any{"dirs": dirs[:2], "label": label}})
			source.Finish("ok", result, err)
			if err != nil || result != `["executed","executed"]` {
				t.Fatalf("source = %s, %v", result, err)
			}
			entries, err := runstore.ReadJournal(source.Meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[0].Key == entries[1].Key {
				t.Fatal("cwd was omitted from journal identity")
			}
			for i := range entries {
				if entries[i].Cwd != dirs[i] || entries[i].Key != profileCallKey(store.Profiles[0], prompt, "", dirs[i], label) {
					t.Fatalf("journal identity for directory %s: %+v", dirs[i], entries[i])
				}
				// Distinct cached values prove replay follows identity, not order.
				entries[i].Result = filepath.Base(entries[i].Cwd)
			}
			cache := NewCache(entries)
			result, err = Execute(ctx, Options{ScriptSrc: script, Store: store, WorkDir: base, Args: map[string]any{"dirs": []string{dirs[1], dirs[2], dirs[0]}, "label": label}, Cache: cache})
			if err != nil || result != `["b","executed","a"]` || cache.Stats().Hits != 2 {
				t.Fatalf("reordered resume with cwd change = %s, %v, %#v", result, err, cache.Stats())
			}
		})
	}
}

func TestExecuteResumeRequiresMatchingExecutionConfiguration(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_config-source")
	base := t.TempDir()
	original := profile.Profile{Name: "local", Harness: profile.HarnessCustom, Model: "first",
		Command: []string{"/bin/sh", "-c", "printf fresh"}, Env: map[string]string{"TOKEN": "private-token"}}
	script := `return await agent("task", {profile:"local",label:"job"});`
	source, err := runstore.Create("config source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Execute(ctx, Options{ScriptSrc: script, Store: &profile.Store{Profiles: []profile.Profile{original}}, Run: source, WorkDir: base})
	source.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal = %v, %v", entries, err)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-token") {
		t.Fatal("profile secret leaked into journal")
	}
	entries = append(entries,
		runstore.JournalEntry{Key: legacyCallKey(original.Name, "task", ""), Label: "job", Result: "stale"},
		runstore.JournalEntry{Key: "v2:historical", Label: "job", Result: "stale"})
	for _, tc := range []struct {
		name   string
		change func(*profile.Profile)
		hits   int
	}{
		{"unchanged", func(p *profile.Profile) {}, 1},
		{"model", func(p *profile.Profile) { p.Model = "second" }, 0},
		{"model chain", func(p *profile.Profile) { p.Models = []string{"first", "fallback"} }, 0},
		{"harness", func(p *profile.Profile) { p.Harness = profile.HarnessMock }, 0},
		{"args", func(p *profile.Profile) { p.ExtraArgs = []string{"--different"} }, 0},
		{"env", func(p *profile.Profile) { p.Env = map[string]string{"TOKEN": "new-private-token"} }, 0},
		{"command", func(p *profile.Profile) { p.Command = []string{"/bin/sh", "-c", "printf changed"} }, 0},
		{"safety", func(p *profile.Profile) { p.SafeMode = true }, 0},
		{"timeout", func(p *profile.Profile) { p.TimeoutSec = 1 }, 0},
		{"equivalent models", func(p *profile.Profile) { p.Models = []string{"first"} }, 1},
		{"description", func(p *profile.Profile) { p.Description = "new description"; p.Taste = 9 }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := original
			tc.change(&p)
			cache := NewCache(entries)
			_, err := Execute(ctx, Options{ScriptSrc: script, Store: &profile.Store{Profiles: []profile.Profile{p}}, WorkDir: base, Cache: cache})
			if err != nil {
				t.Fatal(err)
			}
			if cache.Stats().Hits != tc.hits {
				t.Fatalf("cache hits = %d, want %d", cache.Stats().Hits, tc.hits)
			}
		})
	}
}

func TestExecutionConfigDigestUsesEffectiveSettings(t *testing.T) {
	original := profile.Profile{Harness: profile.HarnessCustom, Model: "first", Models: []string{"first", "fallback"}, Env: map[string]string{"A": "one", "B": "two"}}
	base := executionConfigDigest(original, time.Minute)
	for _, tc := range []struct {
		name   string
		change func(*profile.Profile)
	}{
		{"runtime version", func(p *profile.Profile) { p.RuntimeVersion = "2" }},
		{"runtime executable", func(p *profile.Profile) { p.RuntimeExecutable = "/other/executable" }},
		{"runtime skills", func(p *profile.Profile) { p.RuntimeSkillPaths = []string{"/other/skill"} }},
		{"output", func(p *profile.Profile) {
			p.Output = &workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON}
		}},
		{"chain order", func(p *profile.Profile) { p.Models = []string{"fallback", "first"} }},
		{"fallback model", func(p *profile.Profile) { p.Models = []string{"first", "other"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := original
			tc.change(&p)
			if executionConfigDigest(p, time.Minute) == base {
				t.Fatal("changed execution setting has the same digest")
			}
		})
	}
	if executionConfigDigest(original, 2*time.Minute) == base {
		t.Fatal("changed effective timeout has the same digest")
	}
	p := original
	p.Model = "ignored scalar"
	p.Models = []string{" first ", "fallback", "first"}
	p.Env = map[string]string{"B": "two", "A": "one"}
	p.Description = "description"
	p.Cost = 9
	p.Default = true
	p.MaxConcurrent = 2
	p.MaxCallsPerRun = 10
	if executionConfigDigest(p, time.Minute) != base {
		t.Fatal("equivalent effective settings have different digests")
	}
	p = profile.Profile{Harness: profile.HarnessCustom, Model: "first"}
	q := p
	q.Models = []string{"first"}
	q.Output = p.EffectiveOutput()
	q.Env = map[string]string{}
	q.ExtraArgs = []string{}
	q.Command = []string{}
	if executionConfigDigest(p, time.Minute) != executionConfigDigest(q, time.Minute) {
		t.Fatal("equivalent empty settings have different digests")
	}
}

func TestRetiredProfileAccountPreservesReplayIdentity(t *testing.T) {
	var p profile.Profile
	if err := json.Unmarshal([]byte(`{"name":"retained-worker","harness":"custom","model":"fixture-model","command":["/bin/sh","-c","exit 97"],"timeoutSec":60,"runtimeAccount":"retired-account"}`), &p); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil || strings.Contains(string(raw), "runtimeAccount") {
		t.Errorf("retired account survived profile encoding: %v", err)
	}
	// Freeze the key from the old empty-account profile. Keep exec-v1 and v3 stable.
	const retainedKey = "v3:a6e7ffaef437075ff8c164349c7279bc05b72164c74975fb453ddaa6a34762c6"
	key := callKey(p, "Return the retained result.", "", "/retained/workdir", "completed-job", time.Minute)
	if key != retainedKey {
		t.Errorf("replay key = %s, want %s", key, retainedKey)
	}
	cache := NewCache([]runstore.JournalEntry{{Key: retainedKey, Result: "retained result"}})
	if got, ok := cache.pop(key); !ok || got != "retained result" {
		t.Errorf("retained journal result = %v, %v", got, ok)
	}
}
