package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
)

func generationFixture(t *testing.T, h string) (*Runtime, string, string) {
	t.Helper()
	accounts := testAccounts(t)
	n, err := accounts.Create(h, "generation fixture")
	if err != nil {
		t.Fatal(err)
	}
	home, err := accounts.HarnessHome(h)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: accounts, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(store.DataDir(), "harnesses", "generations")}
	t.Setenv("DYNA_BIN", "/fake/dyna")
	t.Cleanup(func() { _ = removeBundle(r.Generations) })
	return r, home, n.ID
}

// The skills view pins the current generation of harnesses with no config file.
func addGenerationSkill(t *testing.T, r *Runtime, h string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Config.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedSkill{Name: "base", Source: source, EnabledHarnesses: []string{h}}); err != nil {
		t.Fatal(err)
	}
}
func buildGeneration(t *testing.T, r *Runtime, h, home, id string) harnessmgr.ConfigGeneration {
	t.Helper()
	g, err := r.generate(h, home, id, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func addGenerationMCP(t *testing.T, r *Runtime, h, name string) {
	t.Helper()
	if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: name, MCPDefinition: harnessmgr.MCPDefinition{Command: name}, EnabledHarnesses: []string{h}}); err != nil {
		t.Fatal(err)
	}
}
func assertGenerationExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("protected generation disappeared: %s: %v", path, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("unused generation remains: %s: %v", path, err)
	}
}
func leaseGeneration(t *testing.T, r *Runtime, dir string) *GenerationLease {
	t.Helper()
	unlock, err := r.generationLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	lease, err := r.leaseGenerationLocked(dir)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
func writeGenerationSession(t *testing.T, name string, pin Pin, closed bool) {
	t.Helper()
	meta := map[string]any{"runtimePin": pin}
	if closed {
		meta["closed"] = time.Now()
	}
	recordtest.Write(t, filepath.Join(store.DataDir(), "sessions"), name, meta)
}

func TestGenerationContentReuseAndSecretRotation(t *testing.T) {
	for _, h := range []string{"claude-code", "codex", "opencode", "pi", "muse"} {
		t.Run(h, func(t *testing.T) {
			r, home, id := generationFixture(t, h)
			t.Setenv("MCP_TOKEN", "first-secret-canary")
			if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: "tools", MCPDefinition: harnessmgr.MCPDefinition{Command: "tools", EnvKeys: []string{"MCP_TOKEN"}}, EnabledHarnesses: []string{h}}); err != nil {
				t.Fatal(err)
			}
			a := buildGeneration(t, r, h, home, id)
			if !privateGenerationRoots(a.Env, home) {
				t.Fatalf("%s generation lacks private HOME/XDG roots", h)
			}
			t.Setenv("MCP_TOKEN", "second-secret-canary")
			b := buildGeneration(t, r, h, home, id)
			if a.Directory != b.Directory || strings.Join(a.Args, "\x00") != strings.Join(b.Args, "\x00") {
				t.Fatal("equal effective inputs did not reuse the generation")
			}
			if b.Env["MCP_TOKEN"] != "second-secret-canary" {
				t.Fatal("reuse did not resolve the current secret")
			}
			if _, err := validateOwnedGeneration(a.Directory); err != nil {
				t.Fatal(err)
			}
			err := filepath.WalkDir(a.Directory, func(path string, e os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if e.Type().IsRegular() {
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if strings.Contains(string(raw), "secret-canary") {
						t.Fatalf("secret stored in %s", path)
					}
					if strings.Contains(string(raw), ".generation-stage-") || strings.Contains(string(raw), ".generation-input-") {
						t.Fatalf("staging path stored in %s", path)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.generate(h, home, id, func(string) (string, bool) { return "", false }); err == nil {
				t.Fatal("reuse accepted a missing secret reference")
			}
		})
	}
}

func TestGenerationSkillSnapshotAndEffectiveInputs(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	source := t.TempDir()
	skill := filepath.Join(source, "SKILL.md")
	resource := filepath.Join(source, "helper.sh")
	for path, text := range map[string]string{skill: "skill v1", resource: "echo v1"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Config.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedSkill{Name: "custom", Source: source, EnabledHarnesses: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	a := buildGeneration(t, r, "codex", home, id)
	if err := os.Chtimes(resource, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	b := buildGeneration(t, r, "codex", home, id)
	if a.Directory != b.Directory {
		t.Fatal("mtime invalidated generation")
	}
	addGenerationMCP(t, r, "claude-code", "unrelated")
	b = buildGeneration(t, r, "codex", home, id)
	if a.Directory != b.Directory {
		t.Fatal("another harness invalidated generation")
	}
	if err := os.WriteFile(resource, []byte("echo v2"), 0600); err != nil {
		t.Fatal(err)
	}
	c := buildGeneration(t, r, "codex", home, id)
	if c.Directory == a.Directory {
		t.Fatal("resource change did not invalidate generation")
	}
	old, err := os.ReadFile(filepath.Join(a.Directory, "skills", "custom", "helper.sh"))
	if err != nil || string(old) != "echo v1" {
		t.Fatal("source change modified retained generation")
	}
	if err = os.Chmod(resource, 0700); err != nil {
		t.Fatal(err)
	}
	d := buildGeneration(t, r, "codex", home, id)
	if d.Directory == c.Directory {
		t.Fatal("executable mode did not invalidate generation")
	}
	checkMode(t, filepath.Join(d.Directory, "skills", "custom", "helper.sh"), 0700)
	// Reuse must republish the current shared view; resume must leave it alone.
	if err = os.Chmod(resource, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(resource, []byte("echo v1"), 0600); err != nil {
		t.Fatal(err)
	}
	reused := buildGeneration(t, r, "codex", home, id)
	if reused.Directory != a.Directory {
		t.Fatal("restored content did not reuse original")
	}
	if _, err = loadNativeGeneration("codex", c.Directory, os.LookupEnv); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(home, "skills", "custom"))
	if err != nil || target != filepath.Join(a.Directory, "skills", "custom") {
		t.Fatal("resume changed the current skill view", target, err)
	}
}

func TestGenerationCapturedSkillsSurviveSourceEdits(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Config.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedSkill{Name: "custom", Source: source, EnabledHarnesses: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureCatalog(); err != nil {
		t.Fatal(err)
	}
	unlock, err := r.generationLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	input, err := os.MkdirTemp(r.Generations, ".input-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer removeBundle(input)
	snapshot, inputs, err := r.captureGeneration("claude-code", home, id, input, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(r.Generations, ".stage-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer removeBundle(stage)
	if _, err = r.Config.GenerateSnapshotAt("claude-code", home, stage, filepath.Join(r.Generations, "published"), snapshot, os.LookupEnv, inputs.Preferences); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(stage, "plugin", "skills", "custom", "SKILL.md"))
	if err != nil || string(raw) != "before" {
		t.Fatal("materialization reread mutable source")
	}
}

func TestGenerationPreferencesAndCatalogOrder(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	addGenerationMCP(t, r, "codex", "zulu")
	addGenerationMCP(t, r, "codex", "alpha")
	a := buildGeneration(t, r, "codex", home, id)
	c, err := r.Config.Read()
	if err != nil {
		t.Fatal(err)
	}
	c.MCP[0], c.MCP[1] = c.MCP[1], c.MCP[0]
	if err = writeGenerationJSON(r.Config.Path, c, 0600); err != nil {
		t.Fatal(err)
	}
	b := buildGeneration(t, r, "codex", home, id)
	if a.Directory != b.Directory {
		t.Fatal("catalog order invalidated effective content")
	}
	root, err := r.preferenceRoot()
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(root, "layer-test", "settings.json")
	if err = writeGenerationJSON(settings, map[string]any{"tui.animations": false, "history.persistence": "none"}, 0600); err != nil {
		t.Fatal(err)
	}
	if err = preferences.Save(id, preferences.Ref{Settings: settings, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	withPrefs := buildGeneration(t, r, "codex", home, id)
	if withPrefs.Directory == a.Directory {
		t.Fatal("effective preferences did not invalidate content")
	}
	if !strings.Contains(strings.Join(withPrefs.Args, " "), "tui.animations=false") {
		t.Fatal("preference missing from generated args")
	}
	again := buildGeneration(t, r, "codex", home, id)
	if withPrefs.Directory != again.Directory {
		t.Fatal("preferences did not reuse")
	}
	loaded, err := loadNativeGeneration("codex", a.Directory, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(loaded.Args, " "), "tui.animations") {
		t.Fatal("resume loaded current preferences")
	}
}

func TestGenerationGCKeepsAllReferencesAndPreferences(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	var gens []harnessmgr.ConfigGeneration
	addGenerationSkill(t, r, "codex")
	for _, name := range []string{"open", "closed", "active", "released", "current"} {
		addGenerationMCP(t, r, "codex", name)
		gens = append(gens, buildGeneration(t, r, "codex", home, id))
	}
	writeGenerationSession(t, "open", Pin{Generation: gens[0].Directory}, false)
	writeGenerationSession(t, "closed", Pin{Generation: gens[1].Directory}, true)
	active := leaseGeneration(t, r, gens[2].Directory)
	defer active.Close()
	released := leaseGeneration(t, r, gens[3].Directory)
	if err := released.Close(); err != nil {
		t.Fatal(err)
	}
	sentinels := []string{filepath.Join(r.Generations, "preferences", "layer-old", "settings.json"), filepath.Join(r.Generations, "native-legacy", "native-generation.json"), filepath.Join(r.Generations, "unknown", "keep")}
	for _, path := range sentinels {
		if err := writeGenerationJSON(path, map[string]string{"sentinel": "unchanged"}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, g := range gens {
		assertGenerationExists(t, g.Directory, i == 0 || i == 2 || i == 4)
	}
	for _, path := range sentinels {
		raw, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(raw), "unchanged") {
			t.Fatal("cleanup changed unrelated content", path, err)
		}
	}
}

func TestGenerationLeaseNativeProbeAndRunnerRelease(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	g := buildGeneration(t, r, "claude-code", home, id)
	lease := leaseGeneration(t, r, g.Directory)
	if err := lease.Bind(json.RawMessage(`{"guard":"native"}`)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []GenerationProcessProbe{nil, func(json.RawMessage) (bool, error) { return true, nil }, func(json.RawMessage) (bool, error) { return false, errors.New("unknown") }} {
		if err := r.GCGenerationsWithProbe(context.Background(), probe); err != nil {
			t.Fatal(err)
		}
		assertGenerationExists(t, g.Directory, true)
	}
	if err := r.GCGenerationsWithProbe(context.Background(), func(ref json.RawMessage) (bool, error) {
		if string(ref) != `{"guard":"native"}` {
			t.Fatalf("changed opaque reference: %s", ref)
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, false)
	if err := lease.Bind(json.RawMessage(`{}`)); err == nil {
		t.Fatal("closed lease accepted a binding")
	}
}

func TestGenerationLeaseDeadOwnerAndBindingReplacement(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	g := buildGeneration(t, r, "claude-code", home, id)
	lease := leaseGeneration(t, r, g.Directory)
	if err := lease.Bind(json.RawMessage(`{"attempt":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Bind(json.RawMessage(`{"attempt":2}`)); err != nil {
		t.Fatal(err)
	}
	// A dead runner does not release a surviving guarded process.
	lease.use.OwnerPID = 1 << 30
	if err := writePrivateAccountJSON(lease.path, lease.use); err != nil {
		t.Fatal(err)
	}
	if err := r.GCGenerationsWithProbe(context.Background(), func(ref json.RawMessage) (bool, error) {
		if string(ref) != `{"attempt":2}` {
			t.Fatal("old reference survived Bind")
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, true)
	if err := r.GCGenerationsWithProbe(context.Background(), func(json.RawMessage) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, false)
	// An abandoned pre-start record with a dead owner can expire.
	g = buildGeneration(t, r, "claude-code", home, id)
	prestart := leaseGeneration(t, r, g.Directory)
	prestart.use.OwnerPID = 1 << 30
	if err := writePrivateAccountJSON(prestart.path, prestart.use); err != nil {
		t.Fatal(err)
	}
	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, false)
}

func TestGenerationGCScanFailuresDeleteNothing(t *testing.T) {
	for _, failure := range []string{"session", "lease", "candidate-link", "root-link"} {
		t.Run(failure, func(t *testing.T) {
			r, home, id := generationFixture(t, "claude-code")
			a := buildGeneration(t, r, "claude-code", home, id)
			addGenerationMCP(t, r, "claude-code", "later")
			b := buildGeneration(t, r, "claude-code", home, id)
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "session":
				recordtest.WriteRaw(t, filepath.Join(store.DataDir(), "sessions"), "broken", []byte("{"))
			case "lease":
				lease := leaseGeneration(t, r, a.Directory)
				if err := os.WriteFile(lease.path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "candidate-link":
				if err := removeBundle(b.Directory); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), b.Directory); err != nil {
					t.Fatal(err)
				}
			case "root-link":
				original := r.Generations
				r.Generations = filepath.Join(t.TempDir(), "linked")
				if err := os.Symlink(original, r.Generations); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.GCGenerations(context.Background()); err == nil {
				t.Fatal("unsafe scan succeeded")
			}
			assertGenerationExists(t, a.Directory, true)
			raw, err := os.ReadFile(outside)
			if err != nil || string(raw) != "keep" {
				t.Fatal("cleanup affected external sentinel")
			}
		})
	}
}

func TestGenerationReuseRebuildsTamperingAndRejectsSkillLinks(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	addGenerationSkill(t, r, "codex")
	g := buildGeneration(t, r, "codex", home, id)
	original, err := os.ReadFile(filepath.Join(g.Directory, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.Directory, "mcp.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if rebuilt := buildGeneration(t, r, "codex", home, id); rebuilt.Directory != g.Directory {
		t.Fatal("rebuild moved the generation")
	}
	if raw, err := os.ReadFile(filepath.Join(g.Directory, "mcp.json")); err != nil || string(raw) != string(original) {
		t.Fatal("reused corrupt generation", string(raw), err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(g.Directory, "mcp.json"), filepath.Join(source, "resource")); err != nil {
		t.Fatal(err)
	}
	if err := r.Config.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedSkill{Name: "unsafe", Source: source, EnabledHarnesses: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.generate("codex", home, id, os.LookupEnv); err == nil {
		t.Fatal("accepted interior skill symlink")
	}
	entries, err := os.ReadDir(filepath.Join(home, "skills"))
	if err != nil || len(entries) != 1 {
		t.Fatal("failed generation changed shared view", entries, err)
	}
	target, err := os.Readlink(filepath.Join(home, "skills", entries[0].Name()))
	if err != nil || target != filepath.Join(g.Directory, "skills", entries[0].Name()) {
		t.Fatal("failed generation changed shared view", target, err)
	}
}

func TestGenerationLifecycleSelectionAndLeaseAreAtomic(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	unlock, err := r.generationLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g, err := r.generateLocked("claude-code", home, id, os.LookupEnv)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- r.GCGenerations(context.Background()) }()
	<-started
	lease, err := r.leaseGenerationLocked(g.Directory)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, true)
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err = r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, false)
}

func TestPrepareLeasedKeepsGenerationThroughRunnerLifetime(t *testing.T) {
	r, p := runtimeFixture(t, "claude-code")
	_, pin, lease, err := r.PrepareLeased(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil {
		t.Fatal("PrepareLeased returned no lease")
	}
	if err = r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, pin.Generation, true)
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err = r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, pin.Generation, false)
}

func TestConcurrentGenerationReusePublishesOneCompleteDirectory(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	var wg sync.WaitGroup
	paths := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			g, err := r.generate("codex", home, id, os.LookupEnv)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err = validateOwnedGeneration(g.Directory); err != nil {
				t.Error(err)
			}
			paths <- g.Directory
		})
	}
	wg.Wait()
	close(paths)
	first := ""
	for path := range paths {
		if first == "" {
			first = path
		}
		if path != first {
			t.Fatal("concurrent calls published different generations")
		}
	}
	entries, err := os.ReadDir(r.Generations)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".generation-") {
			t.Fatal("staging directory remained", entry.Name())
		}
	}
}

func writeGenerationJSON(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return store.WriteJSON(path, value, mode)
}

func TestGenerationGCProtectsPartialSharedConfigPublication(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	a := buildGeneration(t, r, "pi", home, id)
	// The old skill view can disappear while the shared config still refers to A.
	// A failed config replacement must preserve both the old and pending targets.
	if err := os.RemoveAll(filepath.Join(home, "skills")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "mcp.json")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, config); err != nil {
		t.Fatal(err)
	}
	addGenerationMCP(t, r, "pi", "changed")
	pending, err := r.generate("pi", home, id, os.LookupEnv)
	if err == nil {
		t.Fatal("unsafe shared config publication succeeded")
	}
	if pending.Directory == "" || pending.Directory == a.Directory {
		t.Fatal("test did not produce a complete pending generation")
	}
	if err = r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, a.Directory, true)
	assertGenerationExists(t, pending.Directory, true)
	raw, err := os.ReadFile(outside)
	if err != nil || string(raw) != "unchanged" {
		t.Fatal("publication changed external content")
	}
}

// An older build published Muse args as one whole-map pair under the previous
// inputs version. The current build must not reuse that generation.
func TestGenerationNotReusedAcrossInputsVersions(t *testing.T) {
	r, home, id := generationFixture(t, "muse")
	addGenerationMCP(t, r, "muse", "tools")
	fresh := buildGeneration(t, r, "muse", home, id)
	inputDir := t.TempDir()
	_, inputs, err := r.captureGeneration("muse", home, id, inputDir, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	inputs.Version = generationInputsVersion - 1
	digest, err := generationDigest(inputs)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(r.Generations, generationPrefix+digest)
	if stale == fresh.Directory {
		t.Fatal("the inputs version is not part of the content address")
	}
	if err = os.Rename(fresh.Directory, stale); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(stale, "native-generation.json")
	var m nativeGeneration
	if err = readGenerationJSON(manifest, &m); err != nil {
		t.Fatal(err)
	}
	m.Args = []string{"-c", `mcpServers={"tools":{"command":"tools","args":null}}`}
	if err = writeGenerationJSON(manifest, m, 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := generationTreeHash(stale, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeGenerationJSON(filepath.Join(stale, generationMetadata), generationOwnership{1, digest, payload, "muse", home}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = validateOwnedGeneration(stale); err != nil {
		t.Fatal("stale fixture is not a valid generation:", err)
	}
	got := buildGeneration(t, r, "muse", home, id)
	if got.Directory == stale || strings.Join(got.Args, " ") != strings.Join(fresh.Args, " ") {
		t.Fatalf("reused the older generation: %s %q", got.Directory, got.Args)
	}
}

// Codex writes its system skills into CODEX_HOME/skills. That write must land
// in the account home, so the next prepare reuses the sealed generation.
func TestHarnessSkillWritesKeepGenerationSealed(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	g := buildGeneration(t, r, "codex", home, id)
	view := filepath.Join(home, "skills")
	// Older builds linked the whole view to one generation; publication migrates it.
	if err := os.RemoveAll(view); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(g.Directory, "skills"), view); err != nil {
		t.Fatal(err)
	}
	g = buildGeneration(t, r, "codex", home, id)
	system := filepath.Join(view, ".system", "imagegen", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(system), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, []byte("codex system skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if again := buildGeneration(t, r, "codex", home, id); again.Directory != g.Directory {
		t.Fatal("harness skill write invalidated the generation")
	}
	if _, err := validateOwnedGeneration(g.Directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(g.Directory, "skills", ".system")); !os.IsNotExist(err) {
		t.Fatal("harness skill write reached the generation", err)
	}
	if raw, err := os.ReadFile(system); err != nil || string(raw) != "codex system skill" {
		t.Fatal("publication removed the harness's own skills", err)
	}
}

func TestGenerationLifecycleErrorNamesCheckAndPath(t *testing.T) {
	r, home, id := generationFixture(t, "codex")
	g := buildGeneration(t, r, "codex", home, id)
	manifest := filepath.Join(g.Directory, "mcp.json")
	if err := os.WriteFile(manifest, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := validateOwnedGeneration(g.Directory)
	if !errors.Is(err, errGenerationLifecycle) || !errors.Is(err, errGenerationChanged) || !strings.Contains(err.Error(), "files differ from lifecycle.json: "+g.Directory) {
		t.Fatal("error does not name the failed check", err)
	}
	if err = os.Chmod(manifest, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = validateOwnedGeneration(g.Directory)
	if !errors.Is(err, errGenerationChanged) || !strings.Contains(err.Error(), "file mode is not private: "+manifest) {
		t.Fatal("error does not name the failed check", err)
	}
}

func TestGenerationGCDeletesChangedUnpinnedGeneration(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	a := buildGeneration(t, r, "claude-code", home, id)
	defer leaseGeneration(t, r, a.Directory).Close()
	addGenerationMCP(t, r, "claude-code", "later")
	b := buildGeneration(t, r, "claude-code", home, id)
	if err := os.WriteFile(filepath.Join(b.Directory, "settings.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, a.Directory, true)
	assertGenerationExists(t, b.Directory, false)
}
