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
	"dyna-agent/internal/database"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/runtime/genstate"
	"dyna-agent/internal/profile"
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

func TestPiCodeModeGeneration(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	addGenerationMCP(t, r, "pi", "example")
	read := func() map[string]any {
		g := buildGeneration(t, r, "pi", home, id, profile.Profile{})
		raw, err := os.ReadFile(filepath.Join(g.Directory, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := read()
	tools, ok := first["defaultTools"].([]any)
	if !ok || len(tools) != 1 || tools[0] != "+codemode" || first["codemode"].(map[string]any)["mode"] != "only" {
		t.Fatalf("default Pi settings: %v", first)
	}
	if _, ok := first["mcpServers"].(map[string]any)["example"]; !ok {
		t.Fatal("MCP server missing")
	}
	path := filepath.Join(store.ConfigDir(), "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"harness":{"piCodeMode":"off"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	off := read()
	if _, ok := off["defaultTools"]; ok {
		t.Fatalf("off changed default tools: %v", off)
	}
	if _, ok := off["codemode"]; ok {
		t.Fatalf("off changed code mode: %v", off)
	}
	if _, ok := off["mcpServers"].(map[string]any)["example"]; !ok {
		t.Fatal("MCP server missing with off")
	}
}

func buildGeneration(t *testing.T, r *Runtime, h, home, id string, p profile.Profile) harnessmgr.ConfigGeneration {
	t.Helper()
	g, err := r.generate(h, home, id, os.LookupEnv, p)
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
	for _, h := range []string{"claude-code", "pi"} {
		t.Run(h, func(t *testing.T) {
			r, home, id := generationFixture(t, h)
			t.Setenv("MCP_TOKEN", "first-secret-canary")
			if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: "tools", MCPDefinition: harnessmgr.MCPDefinition{Command: "tools", EnvKeys: []string{"MCP_TOKEN"}}, EnabledHarnesses: []string{h}}); err != nil {
				t.Fatal(err)
			}
			a := buildGeneration(t, r, h, home, id, profile.Profile{})
			if !privateGenerationRoots(a.Env, home) {
				t.Fatalf("%s generation lacks private HOME/XDG roots", h)
			}
			t.Setenv("MCP_TOKEN", "second-secret-canary")
			b := buildGeneration(t, r, h, home, id, profile.Profile{})
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
			if _, err = r.generate(h, home, id, func(string) (string, bool) { return "", false }, profile.Profile{}); err == nil {
				t.Fatal("reuse accepted a missing secret reference")
			}
		})
	}
}

func TestGenerationGCKeepsAllReferencesAndPreferences(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	var gens []harnessmgr.ConfigGeneration
	for _, name := range []string{"open", "closed", "active", "released", "current"} {
		addGenerationMCP(t, r, "pi", name)
		gens = append(gens, buildGeneration(t, r, "pi", home, id, profile.Profile{}))
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
	g := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
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
	g := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
	lease := leaseGeneration(t, r, g.Directory)
	if err := lease.Bind(json.RawMessage(`{"attempt":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Bind(json.RawMessage(`{"attempt":2}`)); err != nil {
		t.Fatal(err)
	}
	// A dead runner does not release a surviving guarded process.
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	dead := genstate.Lease{ID: genstate.NewID(), Generation: g.Directory, OwnerPID: 1 << 30, Native: []byte(`{"attempt":2}`)}
	if err := r.generationState().Create(context.Background(), dead); err != nil {
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
	g = buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
	prestart := genstate.Lease{ID: genstate.NewID(), Generation: g.Directory, OwnerPID: 1 << 30}
	if err := r.generationState().Create(context.Background(), prestart); err != nil {
		t.Fatal(err)
	}
	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, false)
}

// A detached runner of an earlier build writes its lease file after the engine
// migrated. Cleanup imports it under the lifecycle lock, so the generation
// stays while that process can still read it.
func TestGenerationGCAdoptsALegacyLeaseWrittenAfterMigration(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	g := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
	uses := filepath.Join(r.Generations, ".uses")
	if err := os.MkdirAll(uses, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Version    int             `json:"version"`
		Generation string          `json:"generation"`
		OwnerPID   int             `json:"ownerPid"`
		Native     json.RawMessage `json:"native,omitempty"`
	}{Version: 1, Generation: g.Directory, OwnerPID: 1 << 30, Native: json.RawMessage(`{"pid":3}`)}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uses, "use-late.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The guarded process still runs, so the generation stays.
	if err := r.GCGenerationsWithProbe(context.Background(), func(ref json.RawMessage) (bool, error) {
		if string(ref) != `{"pid":3}` {
			t.Fatalf("probe reference = %s", ref)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, true)
	// Once the probe is gone, the imported lease no longer protects it.
	if err := r.GCGenerationsWithProbe(context.Background(), func(json.RawMessage) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, g.Directory, false)
}

// A legacy view that a complete publication superseded stops pinning its old
// generation: cleanup imports the unchanged file, does not restore the
// generation, and collects it.
func TestGenerationGCClearsALegacyViewItSuperseded(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	superseded := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	views := filepath.Join(r.Generations, ".views")
	if err := os.MkdirAll(views, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Harness     string
		Home        string
		Generations []string
	}{Harness: "pi", Home: home, Generations: []string{superseded.Directory}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(views, "pi-view.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// A complete publication replaces the view, as the new build records it.
	addGenerationMCP(t, r, "pi", "later")
	current := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	if err := r.recordGenerationView("pi", home, current.Directory, false); err != nil {
		t.Fatal(err)
	}
	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, superseded.Directory, false)
	assertGenerationExists(t, current.Directory, true)
}

func TestGenerationGCScanFailuresDeleteNothing(t *testing.T) {
	for _, failure := range []string{"session", "lease", "candidate-link", "root-link"} {
		t.Run(failure, func(t *testing.T) {
			r, home, id := generationFixture(t, "claude-code")
			a := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
			addGenerationMCP(t, r, "claude-code", "later")
			b := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "session":
				recordtest.WriteRaw(t, filepath.Join(store.DataDir(), "sessions"), "broken", []byte("{"))
			case "lease":
				// A lease index that cannot be read stops every deletion.
				if err := genstate.Migrate(store.DataDir()); err != nil {
					t.Fatal(err)
				}
				db, err := database.Open(store.DataDir())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DROP TABLE generation_leases`); err != nil {
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

func TestGenerationReuseRebuildsTampering(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	g := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	original, err := os.ReadFile(filepath.Join(g.Directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.Directory, "settings.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if rebuilt := buildGeneration(t, r, "pi", home, id, profile.Profile{}); rebuilt.Directory != g.Directory {
		t.Fatal("rebuild moved the generation")
	}
	if raw, err := os.ReadFile(filepath.Join(g.Directory, "settings.json")); err != nil || string(raw) != string(original) {
		t.Fatal("reused corrupt generation", string(raw), err)
	}
}

func TestGenerationLifecycleSelectionAndLeaseAreAtomic(t *testing.T) {
	r, home, id := generationFixture(t, "claude-code")
	unlock, err := r.generationLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g, err := r.generateLocked("claude-code", home, id, os.LookupEnv, profile.Profile{})
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
	r, home, id := generationFixture(t, "pi")
	var wg sync.WaitGroup
	paths := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			g, err := r.generate("pi", home, id, os.LookupEnv, profile.Profile{})
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
	a := buildGeneration(t, r, "pi", home, id, profile.Profile{})
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
	pending, err := r.generate("pi", home, id, os.LookupEnv, profile.Profile{})
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

// An older build published different args under the previous inputs version.
// The current build must not reuse that generation.
func TestGenerationNotReusedAcrossInputsVersions(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	addGenerationMCP(t, r, "pi", "tools")
	fresh := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	_, inputs, err := r.captureGeneration("pi", home, id, os.LookupEnv, profile.Profile{})
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
	m.Args = []string{"--stale-generation-args"}
	if err = writeGenerationJSON(manifest, m, 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := generationTreeHash(stale, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeGenerationJSON(filepath.Join(stale, generationMetadata), generationOwnership{1, digest, payload, "pi", home}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = validateOwnedGeneration(stale); err != nil {
		t.Fatal("stale fixture is not a valid generation:", err)
	}
	got := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	if got.Directory == stale || strings.Join(got.Args, " ") != strings.Join(fresh.Args, " ") {
		t.Fatalf("reused the older generation: %s %q", got.Directory, got.Args)
	}
}

// A harness that writes its own skills into the private home must not
// invalidate the sealed generation.
func TestHarnessSkillWritesKeepGenerationSealed(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	g := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	view := filepath.Join(home, "skills")
	// Older builds linked the whole view to one generation; publication migrates it.
	if err := os.RemoveAll(view); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(g.Directory, "skills"), view); err != nil {
		t.Fatal(err)
	}
	g = buildGeneration(t, r, "pi", home, id, profile.Profile{})
	system := filepath.Join(view, ".system", "imagegen", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(system), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, []byte("harness system skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if again := buildGeneration(t, r, "pi", home, id, profile.Profile{}); again.Directory != g.Directory {
		t.Fatal("harness skill write invalidated the generation")
	}
	if _, err := validateOwnedGeneration(g.Directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(g.Directory, "skills", ".system")); !os.IsNotExist(err) {
		t.Fatal("harness skill write reached the generation", err)
	}
	if raw, err := os.ReadFile(system); err != nil || string(raw) != "harness system skill" {
		t.Fatal("publication removed the harness's own skills", err)
	}
}

func TestGenerationLifecycleErrorNamesCheckAndPath(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	g := buildGeneration(t, r, "pi", home, id, profile.Profile{})
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
	a := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
	defer leaseGeneration(t, r, a.Directory).Close()
	addGenerationMCP(t, r, "claude-code", "later")
	b := buildGeneration(t, r, "claude-code", home, id, profile.Profile{})
	if err := os.WriteFile(filepath.Join(b.Directory, "settings.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGenerationExists(t, a.Directory, true)
	assertGenerationExists(t, b.Directory, false)
}

// A generation and a view of a harness this release no longer runs must not
// stop cleanup: both are still Dyna's, and either would otherwise block every
// other harness's collection forever.
func TestGenerationGCSkipsAReplacedHarness(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	addGenerationMCP(t, r, "pi", "retired-tool")
	retired := buildGeneration(t, r, "pi", home, id, profile.Profile{})

	// Rename the retired copy, and a view of it, the way an old Codex install
	// names them. Its directory name and payload hash stay valid, so only the
	// harness checks can reject them.
	path := filepath.Join(retired.Directory, generationMetadata)
	var meta generationOwnership
	if err := readGenerationJSON(path, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Harness = "codex"
	if err := writeGenerationJSON(path, meta, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateOwnedGeneration(retired.Directory); !errors.Is(err, errGenerationUnsupported) {
		t.Fatalf("a replaced harness was not reported as unsupported: %v", err)
	}
	if err := r.generationState().RecordView(context.Background(), genstate.View{Harness: "codex", Home: home, Generations: []string{retired.Directory}}, false); err != nil {
		t.Fatal(err)
	}

	// A later Pi generation republishes the view, so nothing points at the
	// retired copy any more.
	addGenerationMCP(t, r, "pi", "live-tool")
	live := buildGeneration(t, r, "pi", home, id, profile.Profile{})
	writeGenerationSession(t, "open", Pin{Generation: live.Directory}, false)
	if live.Directory == retired.Directory {
		t.Fatal("the fixture built one directory twice")
	}

	if err := r.GCGenerations(context.Background()); err != nil {
		t.Fatal(err)
	}
	views, err := r.generationState().Views(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Harness == "codex" {
			t.Fatalf("a view of a replaced harness survived: %+v", view)
		}
	}
	assertGenerationExists(t, retired.Directory, false)
	assertGenerationExists(t, live.Directory, true)
}
