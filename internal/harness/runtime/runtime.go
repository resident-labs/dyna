// Package runtime selects private harness bundles and native account scopes.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dyna-agent/internal/account/native"
	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store"
	"dyna-agent/internal/terminalsvc"
)

type Pin struct {
	Bundle     Bundle `json:"bundle"`
	AccountID  string `json:"accountId"` // observed active account; never binds resume
	Generation string `json:"generation"`
}
type Doctor struct {
	Profile       string   `json:"profile"`
	Harness       string   `json:"harness"`
	Binary        string   `json:"binary"`
	Version       string   `json:"version"`
	Home          string   `json:"home"`
	AccountID     string   `json:"accountId"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	SwitchPending bool     `json:"switchPending"`
	RunningTurns  int      `json:"runningTurns"`
	Warnings      []string `json:"warnings,omitempty"`
}
type Runtime struct {
	privateRecommendations func(context.Context) []RecommendedVersion
	privateSwitch          privateRuntimeControls
	privateLoginMu         sync.Mutex
	privateLogins          map[string]*privateRuntimeLogin
	terminals              *terminalsvc.Service
	lifetime               context.Context
	Bundles                *BundleManager
	Accounts               *Accounts
	Config                 *harnessmgr.ConfigManager
	Generations            string
}

func New() *Runtime {
	return &Runtime{Bundles: NewBundleManager(), Accounts: NewAccounts(), Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(runtimeDataDir(), "harnesses", "generations")}
}
func (r *Runtime) Doctor(ctx context.Context, p profile.Profile) Doctor {
	d := Doctor{Profile: p.Name, Harness: p.Harness}
	if !p.DynaManaged() {
		d.Status, d.Reason = "direct", "This harness runs its own command. Dyna does not manage a copy of it."
		return d
	}
	a, err := Adapter(p.Harness)
	if err != nil {
		d.Status = "blocked"
		d.Reason = err.Error()
		return d
	}
	d.SwitchPending, d.RunningTurns = r.Accounts.SwitchStatus(a.Harness)
	if d.SwitchPending {
		d.Status = "switch pending"
		d.Reason = fmt.Sprintf("switch pending: %d turns running. New %s turns, including agents' turns, are paused until they finish.", d.RunningTurns, a.Harness)
	}
	if (&FirstUse{Bundles: r.Bundles}).Installing(a.Harness) {
		d.Status = "installing"
		d.Reason = "Dyna is installing its private copy of this harness."
		return d
	}
	b, err := r.Bundles.Resolve(a.Harness, p.RuntimeVersion)
	if err != nil {
		d.Status = "blocked"
		d.Reason = err.Error()
		return d
	}
	d.Binary, d.Version = b.Executable, b.Version
	nativeHome, err := r.Accounts.HarnessHome(a.Harness)
	if err != nil {
		d.Status, d.Reason = "blocked", err.Error()
		return d
	}
	d.Home = harnesshome.Home(nativeHome)
	catalog, err := r.Config.Read()
	if err != nil {
		d.Status, d.Reason = "blocked", "The owner tool bridge policy could not be read. Check harness-config.json."
		return d
	}
	d.Warnings = privateHomeWarnings(nativeHome, a.Harness, catalog.ToolHomeBridges)
	active, err := r.Accounts.Active(a.Harness)
	if err != nil {
		d.Status = "not signed in"
		d.Reason = "Sign in and activate an account for this harness."
		return d
	}
	d.AccountID = active.ID
	if a.AccountBlocked != "" {
		d.Status = "blocked"
		d.Reason = a.AccountBlocked
		return d
	}
	if _, err := r.observeAccount(ctx, active.ID, b.Executable); err != nil {
		d.Status = "blocked"
		d.Reason = fmt.Sprintf("The active native login could not be checked: %v", err)
		if NativeLoginNeedsSignIn(err) {
			d.Status = "not signed in"
			d.Reason = "The active native login is unavailable. Sign in again."
		}
		return d
	}
	if d.SwitchPending {
		return d
	}
	d.Status = "ready"
	d.Reason = strings.Join(a.PartialReasons, " ")
	if d.Reason != "" {
		d.Status = "partial"
	}
	return d
}
func (r *Runtime) Prepare(ctx context.Context, p profile.Profile, previous *Pin) (profile.Profile, Pin, error) {
	return r.prepare(ctx, p, previous, nil)
}

// PrepareLeased closes the selection-to-lease race with generation cleanup.
// The caller closes the runner-owned lease after all native attempts drain.
func (r *Runtime) PrepareLeased(ctx context.Context, p profile.Profile, previous *Pin) (profile.Profile, Pin, *GenerationLease, error) {
	var lease *GenerationLease
	selected, pin, err := r.prepare(ctx, p, previous, &lease)
	if err == nil && pin.Bundle.Harness == "opencode" {
		lease.service, err = prepareOpenCodeService(ctx, selected, pin)
		if err == nil {
			err = r.requireAccount(ctx, pin.AccountID, pin.Bundle.Executable)
		}
	}
	if err != nil && lease != nil {
		_ = lease.Close()
		lease = nil
	}
	return selected, pin, lease, err
}

func (r *Runtime) prepare(ctx context.Context, p profile.Profile, previous *Pin, leaseOut **GenerationLease) (profile.Profile, Pin, error) {
	a, err := Adapter(p.Harness)
	if err != nil {
		return p, Pin{}, err
	}
	if a.AccountBlocked != "" {
		return p, Pin{}, errors.New(a.AccountBlocked)
	}
	for k, v := range p.Env {
		if (k == "HOME" || strings.HasPrefix(k, "XDG_")) && v != os.Getenv(k) {
			return p, Pin{}, fmt.Errorf("private runtime cannot override %s", k)
		}
	}
	// Install a missing private copy on first use.
	version := p.RuntimeVersion
	if previous != nil {
		version = ""
	}
	if _, _, err := (&FirstUse{Bundles: r.Bundles}).EnsureBundle(ctx, a.Harness, version); err != nil {
		return p, Pin{}, err
	}
	r.Accounts.RepairAntigravityACPAccount()
	// Dyna never copies a personal login on its own. The user signs in, or
	// chooses the personal login, on the Harnesses page or with dyna harness login.
	active, err := r.Accounts.activeFor(a.Harness, p.RuntimeRepo)
	if err != nil {
		if errors.Is(err, errScopeRefused) {
			return p, Pin{}, err
		}
		return p, Pin{}, fmt.Errorf("Sign in to %s in Dyna.", a.Harness)
	}
	b, err := r.Bundles.Resolve(a.Harness, p.RuntimeVersion)
	pin := Pin{AccountID: active.ID}
	if previous != nil && previous.Bundle.Harness != a.Harness {
		return p, pin, errors.New("native harness changed; start a new session to use it")
	}
	if previous != nil {
		pin = *previous
		pin.AccountID = active.ID
		b, err = r.Bundles.Resolve(a.Harness, previous.Bundle.Version)
		if err != nil {
			b, err = r.Bundles.Resolve(a.Harness, "")
			pin.Generation = ""
		}
	}
	if err != nil {
		return p, pin, err
	}
	pin.Bundle = b
	home, err := r.Accounts.HarnessHome(a.Harness)
	if err != nil {
		return p, pin, err
	}
	if pinsCredentials(a.Harness) {
		if err := r.Accounts.prepareTurnPin(ctx, active, b.Executable); err != nil {
			return p, pin, fmt.Errorf("Sign in to %s in Dyna: %w", a.Harness, err)
		}
	}
	// OpenCode readiness must use the service after its generation is selected.
	// A probe before this step could start the service with an old config.
	if a.Harness != "opencode" || leaseOut == nil {
		if err := r.requireAccount(ctx, active.ID, b.Executable); err != nil {
			return p, pin, err
		}
	}
	env := map[string]string{}
	for k, v := range p.Env {
		env[k] = v
	}
	for k, v := range harnesshome.Discovery(home) {
		env[k] = v
	}
	native, err := r.Accounts.Env(active.ID)
	if err != nil {
		return p, pin, err
	}
	for k, v := range native {
		env[k] = v
	}
	cli := p.Env["DYNA_BIN"]
	if cli == "" {
		cli, _ = os.Executable()
	}
	toolSource := make([]string, 0, len(os.Environ())+len(p.Env))
	toolSource = append(toolSource, os.Environ()...)
	for k, v := range p.Env {
		toolSource = append(toolSource, k+"="+v)
	}
	toolSource, err = r.Config.ToolHomeEnvironment(toolSource)
	if err != nil {
		return p, pin, err
	}
	privateEnv, err := harnesshome.Prepare(home, cli, toolSource)
	if err != nil {
		return p, pin, err
	}
	lookup := func(key string) (string, bool) {
		if key == "DYNA_BIN" {
			return privateEnv[key], true
		}
		if v, ok := p.Env[key]; ok {
			return v, ok
		}
		return os.LookupEnv(key)
	}
	unlock, err := r.generationLock(ctx)
	if err != nil {
		return p, pin, err
	}
	defer unlock()
	var gen harnessmgr.ConfigGeneration
	if pin.Generation != "" {
		gen, err = loadNativeGeneration(a.Harness, pin.Generation, lookup)
		if err == nil && (!privateGenerationRoots(gen.Env, home) || !currentNativeFeaturePolicy(pin.Generation, a.Harness)) {
			// TODO: legacy/shim remove later — rebuild older pins without private HOME/XDG roots or the current native feature policy.
			gen, err = r.generateLocked(a.Harness, home, active.ID, lookup)
		}
	} else {
		gen, err = r.generateLocked(a.Harness, home, active.ID, lookup)
	}
	if err != nil {
		return p, pin, err
	}
	pin.Generation = gen.Directory
	for k, v := range gen.Env {
		env[k] = v
	}
	if a.NativeEnv != nil {
		for k, v := range a.NativeEnv(home) {
			if k == "OPENCODE_CONFIG" {
				continue
			}
			env[k] = v
		}
	}
	// Apply the scope last, including when a resumed generation has old paths.
	for k, v := range privateEnv {
		env[k] = v
	}
	applyNativeFeatureEnvironment(a.Harness, env)
	if err := validateAccountManagerEnv(a.Harness, home, env); err != nil {
		return p, pin, err
	}
	if pinsCredentials(a.Harness) {
		env[pinnedSecretKey] = active.Home
	}
	env["PATH"] = filepath.Dir(b.Executable) + string(os.PathListSeparator) + privateEnv["PATH"]
	paths := []string{}
	_ = filepath.WalkDir(gen.Directory, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == "SKILL.md" {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	p.RuntimeSkillPaths = paths
	p.RuntimeExecutable = b.Executable
	p.Env = env
	p.RuntimePermissionArgs = append([]string{}, p.ExtraArgs...)
	p.ExtraArgs = append(append([]string{}, p.ExtraArgs...), gen.Args...)
	if leaseOut != nil {
		lease, err := r.leaseGenerationLocked(gen.Directory)
		if err != nil {
			return p, pin, err
		}
		*leaseOut = lease
	}
	return p, pin, nil
}

func privateGenerationRoots(env map[string]string, nativeHome string) bool {
	for key, want := range harnesshome.Roots(nativeHome) {
		if value, ok := env[key]; !ok || value != want {
			return false
		}
	}
	return true
}

func privateHomeWarnings(nativeHome, harness string, tools []string) []string {
	warnings := []string{
		"Private tool caches: " + filepath.Join(harnesshome.Home(nativeHome), ".cache"),
		"Owner HOME bridges: " + strings.Join(append([]string{"dyna", "git"}, tools...), ", "),
		"Cloud and registry configuration is unavailable by default. Add trusted CLI names to toolHomeBridges in harness-config.json to use owner configuration.",
	}
	switch harness {
	case "claude-code", "codex", "muse":
		warnings = append(warnings, "A copied login shares its refresh-token family with the personal login. A refresh on either side can require a new sign-in on the other side. Dyna does not repeat the import.")
	}
	if harness == "opencode" {
		warnings = append(warnings, "Legacy native history and snapshots in personal XDG_DATA_HOME/opencode are not imported without proof of Dyna ownership. Dyna transcripts remain available; some old native sessions may need a new session.")
	}
	return warnings
}
func (r *Runtime) observeAccount(ctx context.Context, id, exe string) (NativeAccount, error) {
	n, err := r.Accounts.Find(id)
	if err != nil {
		return n, err
	}
	m, err := r.Accounts.Manager(id, exe)
	if err != nil {
		return n, err
	}
	capture, err := m.Capture(ctx, "")
	if err != nil {
		return n, err
	}
	if len(capture) == 0 {
		return n, native.ErrNotSignedIn
	}
	if n.Identity != "" && capture[0].Identity != n.Identity {
		return n, errors.New("native login identity changed")
	}
	n.Status = "ready"
	return n, nil
}

// Regenerate updates only Dyna's private views. Catalog writes remain the source
// of truth if a missing environment reference delays one native generation.
func (r *Runtime) Regenerate(harness string) error {
	if err := r.EnsureCatalog(); err != nil {
		return err
	}
	failures := []string{}
	for _, a := range RuntimeAdapters() {
		if harness != "" && canonicalRuntimeID(harness) != a.Harness {
			continue
		}
		active, err := r.Accounts.Active(a.Harness)
		if err != nil {
			continue
		}
		if _, err := r.regenerateAccount(active); err != nil {
			failures = append(failures, a.Harness)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("generation is pending for %s; check environment references and adapter status", strings.Join(failures, ", "))
	}
	return nil
}

// RegenerateAccount publishes the same runtime generation used by session launch.
func (r *Runtime) RegenerateAccount(harness, id string) (harnessmgr.ConfigGeneration, error) {
	if err := r.EnsureCatalog(); err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	account, err := r.Accounts.Find(id)
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	h, err := CanonicalHarness(harness)
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	if h != account.Harness {
		return harnessmgr.ConfigGeneration{}, errors.New("private account belongs to another harness")
	}
	return r.regenerateAccount(account)
}

func (r *Runtime) regenerateAccount(account NativeAccount) (harnessmgr.ConfigGeneration, error) {
	home, err := r.Accounts.HarnessHome(account.Harness)
	if err != nil {
		return harnessmgr.ConfigGeneration{}, err
	}
	return r.generate(account.Harness, home, account.ID, os.LookupEnv)
}

// EnsureCatalog retires the orchestrator skill older versions seeded into every
// harness. The skill is for agents outside Dyna; `dyna skill install` owns it.
// TODO: legacy/shim remove later — cleans catalogs seeded by older versions; remove once supported catalogs no longer hold it
func (r *Runtime) EnsureCatalog() error {
	dir := filepath.Join(filepath.Dir(r.Config.Path), "skills", "load-dyna-orchestrator")
	if err := r.Config.RetireManagedSkill("load-dyna-orchestrator", dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
func OpenPins() ([]Bundle, error) {
	var selected struct {
		Profiles []struct {
			Harness string `json:"harness"`
			Version string `json:"runtimeVersion"`
		} `json:"profiles"`
	}
	if err := store.ReadJSON(profile.DefaultPath(), &selected); err != nil {
		return nil, errors.New("Selected runtime versions could not be read; cleanup stopped.")
	}
	out := []Bundle{}
	for _, p := range selected.Profiles {
		if p.Version == "" {
			continue
		}
		h, err := CanonicalHarness(p.Harness)
		if err != nil || !validVersion(p.Version) {
			return nil, errors.New("A selected private runtime version is invalid; cleanup stopped.")
		}
		out = append(out, Bundle{Harness: h, Version: p.Version})
	}
	root := filepath.Join(store.DataDir(), "sessions")
	err := sessionlife.Each(root, true, func(_ string, raw []byte, err error) error {
		var meta struct {
			sessionlife.Snapshot
			RuntimePin *Pin `json:"runtimePin"`
		}
		if err != nil || json.Unmarshal(raw, &meta) != nil {
			return errors.New("session pins could not be read; bundle cleanup stopped")
		}
		if !meta.IsClosed() && meta.RuntimePin != nil {
			out = append(out, meta.RuntimePin.Bundle)
		}
		return nil
	})
	if errors.Is(err, sessionlife.ErrRecord) {
		err = nil // a stray symlink is skipped, as before
	}
	return out, err
}
func AccountInUse(id string) bool {
	n, err := NewAccounts().Find(id)
	if err != nil {
		return false
	}
	active, err := NewAccounts().Active(n.Harness)
	if err != nil || active.ID != id {
		return false
	}
	pending, running := NewAccounts().SwitchStatus(n.Harness)
	return pending || running > 0
}

type Capability struct {
	Harness             string `json:"harness"`
	Status              string `json:"status"`
	Reason              string `json:"reason"`
	InstallSupported    bool   `json:"installSupported"`
	AccountBlocked      string `json:"accountBlocked"`
	MCP                 bool   `json:"mcp"`
	Skills              bool   `json:"skills"`
	SkillsPinned        bool   `json:"skillsPinned"`
	Concurrent          bool   `json:"concurrent"`
	SharedPersonalLogin bool   `json:"sharedPersonalLogin"`
}

func Capabilities() []Capability {
	out := []Capability{}
	for _, a := range RuntimeAdapters() {
		c := Capability{Harness: a.Harness, Status: "ready", Reason: strings.Join(a.PartialReasons, " "), InstallSupported: a.NPMPackage != "" || a.Install != nil, AccountBlocked: a.AccountBlocked, MCP: true, Skills: true, SkillsPinned: a.SkillsPinned, Concurrent: !a.Exclusive}
		if c.Reason != "" {
			c.Status = "partial"
		}
		if a.AccountBlocked != "" {
			c.Status = "blocked"
			c.Reason = a.AccountBlocked + " " + c.Reason
		}
		out = append(out, c)
	}
	return out
}

// SetTerminals connects native sign-in to the process terminal service.
func (r *Runtime) SetTerminals(terminals *terminalsvc.Service) { r.terminals = terminals }

// SetLifetime sets the process context before the runtime starts serving calls.
// Native sign-in and account switches continue when an API request ends.
func (r *Runtime) SetLifetime(ctx context.Context) { r.lifetime = ctx }

func (r *Runtime) lifetimeContext() context.Context {
	if r.lifetime != nil {
		return r.lifetime
	}
	return context.Background()
}

// NativeLoginNeedsSignIn distinguishes rejected or missing credentials from a
// failed probe. Runtime readiness and account status must use the same rule.
func NativeLoginNeedsSignIn(err error) bool {
	return errors.Is(err, native.ErrNotSignedIn) || errors.Is(err, native.ErrInvalidGrant) || errors.Is(err, errPrivateSignIn)
}

func (r *Runtime) requireAccount(ctx context.Context, id, executable string) error {
	if _, err := r.observeAccount(ctx, id, executable); err != nil {
		if NativeLoginNeedsSignIn(err) {
			return fmt.Errorf("the active private account is not signed in: %w", err)
		}
		return fmt.Errorf("the active private account could not be checked: %w", err)
	}
	return nil
}
