package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/goals"
	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/harness/runtime/genstate"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shellmcp"
)

const generationPrefix = "gen-v1-"
const generationMetadata = "lifecycle.json"

var errGenerationLifecycle = errors.New("private generation lifecycle data is unavailable or unsafe")

// errGenerationChanged marks a published generation whose files changed. Its
// content address is still valid, so generateLocked rebuilds it in place.
var errGenerationChanged = errors.New("generation changed after publication")

// errGenerationUnsupported marks a generation of a harness this release no
// longer runs. A turn cannot load it, but the directory is still Dyna's, so
// cleanup may delete it instead of failing for every other harness.
var errGenerationUnsupported = errors.New("generation names a harness this release does not run")

// lifecycleError names the failed check and the path it checked.
func lifecycleError(reason, path string) error {
	return fmt.Errorf("%w: %s: %s", errGenerationLifecycle, reason, path)
}

type generationOwnership struct {
	Version     int    `json:"version"`
	InputHash   string `json:"inputHash"`
	PayloadHash string `json:"payloadHash"`
	Harness     string `json:"harness"`
	Home        string `json:"home"`
}

// dynaMCPName is the generated server that serves Dyna's shell tools.
const dynaMCPName = "dyna"

// generationInputsVersion is part of the content address. Bump it whenever the
// lowering of inputs to native files or args changes, so a generation written
// by an older build is not reused. Version 4: Claude loads the user setting
// source for its private CLAUDE.md. Version 5: native feature policy is part of
// every generation. Version 7: harnesses with DynaShell get Dyna's MCP shell
// server. Version 9: a harness denies its native shell with a plugin hook.
// Version 10: Kimi merges only Dyna-owned tool policy into native config.
// Version 11: Pi's MCP servers carry a request timeout and exposure.
// Version 12: skills are a global catalog and no longer enter a generation.
const generationInputsVersion = 12

type generationInputs struct {
	NativeFeatures                                 nativeFeaturePolicy
	Version                                        int
	Harness, Home, CLI, Format, MCPKey, ConfigFile string
	NativeEnv                                      map[string]string
	PartialReasons                                 []string
	MCP                                            []harnessmgr.ManagedMCP
	Preferences                                    map[string]any
}

func (r *Runtime) generationLock(ctx context.Context) (func(), error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r.Generations == "/" || safeAccountPath(r.Generations) != nil {
		return nil, lifecycleError("generation root is unsafe", r.Generations)
	}
	if err := checkedDir(r.Generations, true); err != nil {
		return nil, err
	}
	info, err := os.Lstat(r.Generations)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return nil, lifecycleError("generation root is not private", r.Generations)
	}
	f, err := acquireAccountLock(ctx, filepath.Join(r.Generations, ".lifecycle.lock"), syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	return func() { releaseAccountLock(f) }, nil
}

func (r *Runtime) generate(h, home, account string, lookup func(string) (string, bool)) (harnessmgr.ConfigGeneration, error) {
	unlock, err := r.generationLock(context.Background())
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	defer unlock()
	return r.generateLocked(h, home, account, lookup)
}

// captureGeneration reads the catalog and preferences into one digest input.
func (r *Runtime) captureGeneration(h, home, account string, lookup func(string) (string, bool)) (harnessmgr.HarnessConfig, generationInputs, error) {
	empty := harnessmgr.HarnessConfig{}
	a, err := Adapter(h)
	if err != nil {
		return empty, generationInputs{}, err
	}
	prefs, err := r.PreferenceValues(account)
	if err != nil {
		return empty, generationInputs{}, err
	}
	if !preferenceLayerSupported(h) {
		prefs = nil
	}
	prefs = nativeFeaturePreferences(h, prefs)
	c, err := r.Config.Read()
	if err != nil {
		return empty, generationInputs{}, err
	}
	snapshot := harnessmgr.HarnessConfig{MCP: []harnessmgr.ManagedMCP{}, Skills: []harnessmgr.ManagedSkill{}}
	inputs := generationInputs{Version: generationInputsVersion, NativeFeatures: nativeFeaturePolicies[h], Harness: h, Home: home, Format: a.ConfigFormat, MCPKey: a.MCPKey, ConfigFile: a.ConfigFile, PartialReasons: a.PartialReasons, Preferences: prefs}
	if a.NativeEnv != nil {
		inputs.NativeEnv = a.NativeEnv(home)
	}
	needsCLI := false
	names := map[string]bool{}
	for _, entry := range c.MCP {
		if !slices.Contains(entry.EnabledHarnesses, h) {
			continue
		}
		if names[entry.Name] {
			return empty, inputs, errors.New("duplicate enabled MCP name")
		}
		names[entry.Name] = true
		d := entry.MCPDefinition
		d.EnvKeys = slices.Clone(d.EnvKeys)
		slices.Sort(d.EnvKeys)
		d.EnvKeys = slices.Compact(d.EnvKeys)
		// A harness without its own CLI materializer needs Dyna's binary for
		// the shell MCP server it starts.
		if h != "claude-code" && len(d.EnvKeys) > 0 {
			needsCLI = true
		}
		snapshot.MCP = append(snapshot.MCP, harnessmgr.ManagedMCP{Name: entry.Name, MCPDefinition: d, EnabledHarnesses: []string{h}})
	}
	dynaShell := nativeFeaturePolicies[h].DynaShell
	if dynaShell && names[dynaMCPName] {
		return empty, inputs, fmt.Errorf("the catalog MCP server %q would replace Dyna's shell server", dynaMCPName)
	}
	if needsCLI || dynaShell {
		if lookup != nil {
			if cli, found := lookup("DYNA_BIN"); found {
				inputs.CLI = cli
			}
		}
		if inputs.CLI == "" {
			inputs.CLI = os.Getenv("DYNA_BIN")
		}
		if inputs.CLI == "" {
			inputs.CLI, _ = exec.LookPath("dyna")
		}
		if !filepath.IsAbs(inputs.CLI) {
			return empty, inputs, errors.New("Dyna CLI is required for MCP environment references and Dyna's shell server")
		}
	}
	if dynaShell {
		snapshot.MCP = append(snapshot.MCP, harnessmgr.ManagedMCP{Name: dynaMCPName, MCPDefinition: harnessmgr.MCPDefinition{Command: inputs.CLI, Args: []string{"mcp", "shell"}, TimeoutSeconds: int(shellmcp.MaxRequestTimeout.Seconds())}, EnabledHarnesses: []string{h}})
	}
	slices.SortFunc(snapshot.MCP, func(a, b harnessmgr.ManagedMCP) int { return strings.Compare(a.Name, b.Name) })
	inputs.MCP = snapshot.MCP
	return snapshot, inputs, nil
}

func generationDigest(inputs generationInputs) (string, error) {
	raw, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (r *Runtime) generateLocked(h, home, account string, lookup func(string) (string, bool)) (harnessmgr.ConfigGeneration, error) {
	h = canonicalRuntimeID(h)
	ownerHome, _ := os.UserHomeDir()
	if home == "/" || home == ownerHome || home == r.Config.Home || safeAccountPath(home) != nil || checkedDir(home, false) != nil || inside(home, r.Generations) || inside(r.Generations, home) {
		return harnessmgr.ConfigGeneration{}, lifecycleError("account home is unsafe for a private generation", home)
	}
	info, err := os.Lstat(home)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return harnessmgr.ConfigGeneration{}, lifecycleError("account home is not private", home)
	}
	if err := r.EnsureCatalog(); err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	snapshot, inputs, err := r.captureGeneration(h, home, account, lookup)
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	digest, err := generationDigest(inputs)
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	published := filepath.Join(r.Generations, generationPrefix+digest)
	resolvedLookup := func(key string) (string, bool) {
		if key == "DYNA_BIN" && inputs.CLI != "" {
			return inputs.CLI, true
		}
		if lookup == nil {
			return "", false
		}
		return lookup(key)
	}
	if _, err = os.Lstat(published); err == nil {
		g, e := loadNativeGeneration(h, published, resolvedLookup)
		if e == nil {
			return g, r.publishGenerationViews(h, home, published)
		}
		if !errors.Is(e, errGenerationChanged) {
			return g, e
		}
		// The inputs still match, so the rebuild restores the same files at the same path.
		log.Printf("rebuilding changed generation: %v", e)
		if err = removeBundle(published); err != nil {
			return g, err
		}
	} else if !os.IsNotExist(err) {
		return harnessmgr.ConfigGeneration{}, err
	}
	staging, err := os.MkdirTemp(r.Generations, ".generation-stage-")
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	defer removeBundle(staging)
	var g harnessmgr.ConfigGeneration
	if h == "claude-code" {
		g, err = r.Config.GenerateSnapshotAt(h, home, staging, published, snapshot, resolvedLookup, inputs.Preferences)
	} else {
		g, err = materializeNative(h, home, staging, published, inputs.CLI, snapshot, inputs.Preferences, resolvedLookup)
	}
	if err != nil {
		return g, err
	}
	applyNativeFeaturePolicy(h, &g)
	if err = os.WriteFile(filepath.Join(staging, nativeFeatureFile), nativeFeaturePolicyBytes(h), 0600); err != nil {
		return g, err
	}
	// The shared Claude materializer retains its native manifest format.
	// This separate, hashed marker gives every harness the same HOME contract.
	if err = writeNativeGeneration(filepath.Join(staging, "private-home.json"), privateGenerationHome{Version: 1, Home: home}); err != nil {
		return g, err
	}
	for key, value := range harnesshome.Roots(home) {
		g.Env[key] = value
	}
	payload, err := generationTreeHash(staging, true)
	if err != nil {
		return g, err
	}
	if err = writeNativeGeneration(filepath.Join(staging, generationMetadata), generationOwnership{1, digest, payload, h, home}); err != nil {
		return g, err
	}
	if err = os.Rename(staging, published); err != nil {
		return g, err
	}
	// Once published, retain the complete generation even if a shared-view update
	// fails. It can be reused or collected later, and no view names staging.
	return g, r.publishGenerationViews(h, home, published)
}

func (r *Runtime) publishGenerationViews(h, home, dir string) error {
	a, err := Adapter(h)
	if err != nil {
		return err
	}
	if safeAccountPath(home) != nil {
		return lifecycleError("account home is unsafe", home)
	}
	if err = removeLegacyGlobalRules(h, home); err != nil {
		return err
	}
	if h == "claude-code" {
		return nil
	}
	file := "settings.json"
	if a.ConfigFormat == "toml" {
		file = "settings.toml"
	}
	if h == "pi" || a.ConfigFile != "" {
		if err = r.recordGenerationView(h, home, dir, true); err != nil {
			return err
		}
	}
	if h == "pi" {
		if err = publishGenerationFile(dir, "pi-mcp.json", home, "mcp.json"); err != nil {
			return err
		}
	}
	if a.ConfigFile != "" {
		if err = publishGenerationFile(dir, file, home, a.ConfigFile); err != nil {
			return err
		}
	}
	if h == "pi" || a.ConfigFile != "" {
		if err = r.recordGenerationView(h, home, dir, false); err != nil {
			return err
		}
	}
	return nil
}

// TODO: legacy/shim remove later — older Dyna versions wrote global rules into
// each harness's user instruction file; global rules now travel in each run's
// instructions. Remove once no account home holds a Dyna-marked file.
var legacyGlobalRulesFiles = map[string]func(home string) string{
	"claude-code": func(home string) string { return filepath.Join(home, "CLAUDE.md") },
	"grok":        func(home string) string { return filepath.Join(home, "AGENTS.md") },
	"pi":          func(home string) string { return filepath.Join(home, "AGENTS.md") },
	"kimi-code":   func(home string) string { return filepath.Join(home, "AGENTS.md") },
}

// removeLegacyGlobalRules deletes the global rules file Dyna once wrote for
// harness h. A file without Dyna's marker belongs to the harness or the user
// and stays.
func removeLegacyGlobalRules(h, home string) error {
	path, ok := legacyGlobalRulesFiles[h]
	if !ok {
		return nil
	}
	target := path(home)
	if (!inside(home, target) && !inside(harnesshome.Home(home), target)) || safeAccountPath(target) != nil {
		return lifecycleError("global rules file is outside the account home", target)
	}
	old, err := os.ReadFile(target)
	if os.IsNotExist(err) || (err == nil && !strings.Contains(string(old), goals.GlobalRulesMarker)) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.Remove(target)
}

func publishGenerationFile(dir, file, home, relative string) error {
	target := filepath.Join(home, relative)
	if !inside(home, target) || safeAccountPath(target) != nil {
		return lifecycleError("generation file view is outside the account home", target)
	}
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return fsutil.WriteFile(target, raw, 0600, false)
}

// generationTreeHash hashes paths, modes, bytes and safe relative links. It
// excludes only our root ownership manifest.
func generationTreeHash(dir string, allowLinks bool) (string, error) {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if allowLinks && rel == generationMetadata {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			if !allowLinks {
				return lifecycleError("generation resource is a symlink", path)
			}
			target, err := os.Readlink(path)
			if err != nil || filepath.IsAbs(target) || !inside(dir, filepath.Clean(filepath.Join(filepath.Dir(path), target))) {
				return lifecycleError("generation link leaves the generation", path)
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !inside(dir, resolved) {
				return lifecycleError("generation link resolves outside the generation", path)
			}
			return encoder.Encode([]any{rel, "link", target})
		}
		if info.Mode().Perm()&0077 != 0 {
			return lifecycleError("file mode is not private", path)
		}
		if info.IsDir() {
			return encoder.Encode([]any{rel, "directory", uint32(mode.Perm())})
		}
		if !mode.IsRegular() {
			return lifecycleError("entry is not a regular file", path)
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		digest := sha256.New()
		if _, err = io.Copy(digest, f); err != nil {
			return err
		}
		return encoder.Encode([]any{rel, "file", uint32(mode.Perm()), hex.EncodeToString(digest.Sum(nil))})
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readGenerationJSON(path string, out any) error {
	if safeAccountPath(path) != nil {
		return lifecycleError("metadata path is unsafe", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4<<20 {
		return lifecycleError("metadata is not a private regular file", path)
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil {
		return err
	}
	if trimmed := strings.TrimSpace(string(raw)); len(trimmed) == 0 || trimmed[0] != '{' {
		return lifecycleError("metadata is not a JSON object", path)
	}
	if json.Unmarshal(raw, out) != nil {
		return lifecycleError("metadata JSON is invalid", path)
	}
	return nil
}

func ownedGenerationName(name string) bool {
	if !strings.HasPrefix(name, generationPrefix) {
		return false
	}
	hash := strings.TrimPrefix(name, generationPrefix)
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == sha256.Size && name == generationPrefix+hex.EncodeToString(raw)
}
func validateOwnedGeneration(dir string) (generationOwnership, error) {
	var meta generationOwnership
	if !ownedGenerationName(filepath.Base(dir)) || checkedDir(dir, false) != nil {
		return meta, lifecycleError("generation name or directory is invalid", dir)
	}
	if err := readGenerationJSON(filepath.Join(dir, generationMetadata), &meta); err != nil {
		return meta, err
	}
	if meta.Version != 1 || filepath.Base(dir) != generationPrefix+meta.InputHash || meta.Home == "" || safeAccountPath(meta.Home) != nil || inside(dir, meta.Home) || inside(meta.Home, dir) {
		return meta, lifecycleError("lifecycle.json does not match the generation", dir)
	}
	if _, err := Adapter(meta.Harness); err != nil {
		return meta, fmt.Errorf("%w: %s", errGenerationUnsupported, dir)
	}
	hash, err := generationTreeHash(dir, true)
	if err != nil {
		return meta, fmt.Errorf("%w: %w", errGenerationChanged, err)
	}
	if hash != meta.PayloadHash {
		return meta, fmt.Errorf("%w: %w: files differ from lifecycle.json: %s", errGenerationLifecycle, errGenerationChanged, dir)
	}
	// A complete native manifest is mandatory for every generation.
	name := "native-generation.json"
	if meta.Harness == "claude-code" {
		name = "generation.json"
	}
	var manifest struct {
		Version     int    `json:"version"`
		Harness     string `json:"harness"`
		Home        string `json:"home"`
		AccountHome string `json:"accountHome"`
	}
	if err = readGenerationJSON(filepath.Join(dir, name), &manifest); err != nil {
		return meta, err
	}
	home := manifest.Home
	if home == "" {
		home = manifest.AccountHome
	}
	if (manifest.Version != 1 && manifest.Version != 2) || canonicalRuntimeID(manifest.Harness) != meta.Harness || home != meta.Home {
		return meta, lifecycleError("native manifest does not match lifecycle.json", dir)
	}
	return meta, nil
}

// GenerationProcessProbe reports whether the opaque native-process reference is
// active. Return an error when liveness is unknown; GC then preserves the lease.
type GenerationProcessProbe func(json.RawMessage) (bool, error)

// GenerationLease is runner-owned. No descriptors are passed to native children.
// Bind replaces the reference only after the preceding attempt has drained.
type GenerationLease struct {
	service *ResourceLease
	runtime *Runtime
	id      string
	use     genstate.Lease
	mu      sync.Mutex
	closed  bool
}

func (r *Runtime) generationState() genstate.Store {
	return genstate.Store{DataDir: runtimeDataDir()}
}

func (r *Runtime) leaseGenerationLocked(dir string) (*GenerationLease, error) {
	if !inside(r.Generations, dir) || filepath.Dir(dir) != r.Generations {
		return nil, lifecycleError("lease names a path outside the generation root", dir)
	}
	lease := genstate.Lease{ID: genstate.NewID(), Generation: dir, OwnerPID: os.Getpid()}
	if err := r.generationState().Create(context.Background(), lease); err != nil {
		return nil, err
	}
	return &GenerationLease{runtime: r, id: lease.ID, use: lease}, nil
}
func (l *GenerationLease) Bind(ref json.RawMessage) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(ref) == 0 || string(ref) == "null" || !json.Valid(ref) {
		return lifecycleError("lease process reference is invalid", l.id)
	}
	unlock, err := l.runtime.generationLock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	next := l.use
	next.Native = append([]byte(nil), ref...)
	if err = l.runtime.generationState().Bind(context.Background(), l.id, next.Native); err != nil {
		return err
	}
	l.use = next
	if l.service != nil {
		return l.service.Bind(ref)
	}
	return nil
}
func (l *GenerationLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.service != nil {
		if err := l.service.Close(); err != nil {
			return err
		}
	}
	unlock, err := l.runtime.generationLock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	next := l.use
	next.Released = true
	// A lease with no process reference cannot protect anything: the native
	// conversation never started, so nothing can still read the generation.
	if len(next.Native) == 0 {
		err = l.runtime.generationState().Delete(context.Background(), l.id)
	} else {
		err = l.runtime.generationState().Release(context.Background(), l.id)
	}
	if err == nil {
		l.closed = true
		l.use = next
	}
	return err
}

// GCGenerations keeps unknown process references until a liveness probe is wired.
func (r *Runtime) GCGenerations(ctx context.Context) error {
	return r.GCGenerationsWithProbe(ctx, NativeProcessAlive)
}

// GCGenerationsWithProbe deletes only recognized content generations. Legacy,
// unknown, staging, and preference directories are outside its deletion scope.
func (r *Runtime) GCGenerationsWithProbe(ctx context.Context, probe GenerationProcessProbe) error {
	unlock, err := r.generationLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	// A detached runner of an earlier build can still write the legacy JSON
	// indexes. Copy them under this lock, before any deletion decision, so a
	// lease that runner creates or rebinds keeps its generation.
	if err := r.generationState().ImportLegacy(ctx, r.Generations); err != nil {
		return err
	}
	entries, err := os.ReadDir(r.Generations)
	if err != nil {
		return err
	}
	candidates := []string{}
	for _, entry := range entries {
		if !ownedGenerationName(entry.Name()) {
			continue
		}
		dir := filepath.Join(r.Generations, entry.Name())
		// A changed generation, or one of a harness this release no longer
		// runs, is still ours to delete when nothing pins it.
		if _, err = validateOwnedGeneration(dir); err != nil && !errors.Is(err, errGenerationChanged) && !errors.Is(err, errGenerationUnsupported) {
			return err
		}
		candidates = append(candidates, dir)
	}
	keep := map[string]bool{}
	if err = r.openGenerationPins(keep); err != nil {
		return err
	}
	if err = r.currentGenerationPins(keep); err != nil {
		return err
	}
	stale, err := r.generationUsePins(keep, probe)
	if err != nil {
		return err
	}
	// All protection metadata and all candidates were checked before any deletion.
	for _, dir := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !keep[dir] {
			if err = removeBundle(dir); err != nil {
				return err
			}
		}
	}
	for _, id := range stale {
		if err = r.generationState().Delete(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runtime) openGenerationPins(keep map[string]bool) error {
	root := filepath.Join(runtimeDataDir(), "sessions")
	if safeAccountPath(root) != nil {
		return lifecycleError("sessions directory is unsafe", root)
	}
	err := sessionlife.Each(root, true, func(_ string, raw []byte, err error) error {
		var meta struct {
			sessionlife.Snapshot
			RuntimePin *Pin `json:"runtimePin"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &meta)
		}
		if err != nil {
			return fmt.Errorf("session pins could not be read; generation cleanup stopped: %w", err)
		}
		if !meta.IsClosed() && meta.RuntimePin != nil && meta.RuntimePin.Generation != "" {
			return r.keepGenerationPath(keep, meta.RuntimePin.Generation)
		}
		return nil
	})
	if errors.Is(err, sessionlife.ErrRecord) {
		return lifecycleError("session entries could not be listed", root)
	}
	return err
}
func (r *Runtime) keepGenerationPath(keep map[string]bool, path string) error {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return lifecycleError("pinned generation path is not clean and absolute", path)
	}
	if filepath.Dir(path) != r.Generations {
		// TODO: legacy/shim remove later — keep data-root aliases in session pins, .views Generations lists, and use-record Generation fields; remove once all three sources contain only canonical paths.
		parent, err := canonicalAccountDataRoot(filepath.Dir(path))
		if err != nil || parent != r.Generations {
			return lifecycleError("pinned generation is outside the generation root", path)
		}
		path = filepath.Join(parent, filepath.Base(path))
	}
	keep[path] = true
	return nil
}
func (r *Runtime) generationUsePins(keep map[string]bool, probe GenerationProcessProbe) ([]string, error) {
	leases, err := r.generationState().Leases(context.Background())
	if err != nil {
		return nil, err
	}
	stale := []string{}
	for _, use := range leases {
		if use.ID == "" || use.OwnerPID <= 0 || use.Generation == "" {
			return nil, lifecycleError("generation use record is invalid", use.ID)
		}
		if err = r.keepGenerationPath(map[string]bool{}, use.Generation); err != nil {
			return nil, err
		}
		alive := !use.Released && syscall.Kill(use.OwnerPID, 0) != syscall.ESRCH
		if len(use.Native) > 0 && string(use.Native) != "null" {
			if probe == nil {
				alive = true
			} else {
				native, probeErr := probe(use.Native)
				alive = alive || native || probeErr != nil
			}
		}
		if alive {
			if err = r.keepGenerationPath(keep, use.Generation); err != nil {
				return nil, err
			}
		} else {
			stale = append(stale, use.ID)
		}
	}
	return stale, nil
}

// Shared config can contain generation paths even when a view update fails.
// Keep its latest complete target independently of the view.
func (r *Runtime) recordGenerationView(h, home, dir string, pending bool) error {
	if h == "" || home == "" || dir == "" {
		return lifecycleError("generation view needs a harness, a home and a generation", dir)
	}
	if err := r.keepGenerationPath(map[string]bool{}, dir); err != nil {
		return err
	}
	state := r.generationState()
	ctx := context.Background()
	view := genstate.View{Harness: h, Home: home, Generations: []string{dir}}
	if pending {
		return state.RecordView(ctx, view, true)
	}
	// A complete publication replaces whatever the legacy view file of this
	// home names. Read those revisions first, so a read failure never leaves
	// a complete view without the supersession that protects it.
	revisions, err := r.legacyViewRevisions(h, home)
	if err != nil {
		return err
	}
	return state.RecordPublication(ctx, view, revisions)
}

// legacyViewRevisions reads the legacy view files that name this harness and
// home, so a complete publication marks exactly the files it replaced.
//
// TODO: legacy/shim remove later — remove with the .views files of earlier
// builds, once no runner of such a build can be alive.
func (r *Runtime) legacyViewRevisions(h, home string) ([]genstate.LegacyRevision, error) {
	dir := filepath.Join(r.Generations, ".views")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []genstate.LegacyRevision{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var view struct {
			Harness, Home string
			Generations   []string
		}
		if json.Unmarshal(raw, &view) != nil || view.Harness != h || view.Home != home {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		out = append(out, genstate.LegacyRevision{Path: path, Digest: hex.EncodeToString(sum[:]), ModifiedNS: info.ModTime().UnixNano()})
	}
	return out, nil
}
func (r *Runtime) currentGenerationPins(keep map[string]bool) error {
	views, err := r.generationState().Views(context.Background())
	if err != nil {
		return err
	}
	for _, view := range views {
		if _, err = Adapter(view.Harness); err != nil {
			// A view of a harness this release no longer runs cannot protect
			// anything, and leaving it would stop cleanup for every harness. Drop
			// it; its generations are collected as unsupported below.
			if err = r.generationState().DeleteView(context.Background(), view.Harness, view.Home); err != nil {
				return err
			}
			continue
		}
		home, e := r.Accounts.HarnessHome(view.Harness)
		if e != nil || view.Home != home && !equivalentAccountHome(r.Accounts.Root, view.Harness, ".runtime", view.Home) {
			return lifecycleError("generation view names another account home", view.Harness+"/"+view.Home)
		}
		if len(view.Generations) == 0 {
			return lifecycleError("generation view lists no generations", view.Harness+"/"+view.Home)
		}
		for _, dir := range view.Generations {
			if err = r.keepGenerationPath(keep, dir); err != nil {
				return err
			}
		}
	}
	return nil
}
