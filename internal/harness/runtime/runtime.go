// Package runtime selects private harness bundles and native account scopes.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store"
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
	privateLoginMu         sync.Mutex
	privateLogins          map[string]*privateRuntimeLogin
	// settlingScratch holds the scratch folders of replaced sign-in flows
	// whose settlement has not finished, so a concurrent sweep spares them.
	settlingScratch map[string]bool
	lifetime        context.Context
	Bundles         *BundleManager
	Accounts        *Accounts
	Config          *harnessmgr.ConfigManager
	Generations     string
}

func New() *Runtime {
	return &Runtime{Bundles: NewBundleManager(), Accounts: NewAccounts(), Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(runtimeDataDir(), "harnesses", "generations")}
}
func (r *Runtime) Doctor(ctx context.Context, p profile.Profile) Doctor {
	d := Doctor{Profile: p.Name, Harness: p.Harness}
	if !p.DynaManaged() {
		d.Status, d.Reason = "direct", "The mock harness runs no external copy, so Dyna manages none."
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
	provider := ProviderForModel(p.Harness, p.Model)
	active, err := r.Accounts.Active(provider, a.Harness)
	if err != nil {
		d.Status = "not signed in"
		d.Reason = "Sign in and activate an account for this harness."
		return d
	}
	d.AccountID = active.ID
	if a.Harness == "pi" && provider == accountstore.ProviderAnthropic && active.Kind != accountstore.KindAPIKey {
		d.Status, d.Reason = "blocked", ErrPiAnthropicOAuth.Error()
		return d
	}
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
	// A profile model names its provider, and the account selection is kept per
	// provider and per room. The room row wins over the global row.
	provider := providerForModel(a.Harness, p.Model)
	// Dyna never copies a personal login on its own. The user signs in, or
	// chooses the personal login, on the Harnesses page or with dyna harness login.
	active, err := r.Accounts.turnAccount(a.Harness, provider, p)
	if err != nil {
		if errors.Is(err, errScopeRefused) || errors.Is(err, ErrPiAnthropicOAuth) {
			return p, Pin{}, err
		}
		return p, Pin{}, fmt.Errorf("Sign in to %s in Dyna.", a.Harness)
	}
	if a.Harness == "pi" && provider == accountstore.ProviderAnthropic && active.Kind != accountstore.KindAPIKey {
		return p, Pin{}, ErrPiAnthropicOAuth
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
	if ok, reason := supportedVersionRange(a.Harness, b.Version); !ok {
		return p, pin, errors.New(reason)
	}
	pin.Bundle = b
	home, err := r.Accounts.HarnessHome(a.Harness)
	if err != nil {
		return p, pin, err
	}
	if a.Harness == "pi" {
		// Pi's history and shared files stay here while the account home holds
		// the credential of the turn.
		if err = ensurePiSharedHome(home); err != nil {
			return p, pin, err
		}
	}
	if pinsCredentials(a.Harness) {
		if err := r.Accounts.prepareTurnPin(ctx, active, b.Executable); err != nil {
			return p, pin, fmt.Errorf("Sign in to %s in Dyna: %w", a.Harness, err)
		}
	}
	if err := r.requireAccount(ctx, active.ID, b.Executable); err != nil {
		return p, pin, err
	}
	env := map[string]string{}
	for k, v := range p.Env {
		env[k] = v
	}
	for k, v := range harnesshome.Discovery(home) {
		env[k] = v
	}
	native, err := r.Accounts.launchEnv(active.ID)
	if err != nil {
		return p, pin, err
	}
	for k, v := range native {
		env[k] = v
	}
	cli := p.Env["DYNA_BIN"]
	if cli == "" {
		cli, _ = fsutil.StableExecutable()
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
	// Discovery no longer runs before the generation lock; a pin re-checked
	// under the lock that turns out unusable is simply rebuilt.
	needsGeneration := pin.Generation == ""
	if !needsGeneration {
		prior, loadErr := loadNativeGeneration(a.Harness, pin.Generation, lookup)
		needsGeneration = loadErr == nil && !pinnedGenerationUsable(a.Harness, pin.Generation, prior, home, privateEnv["DYNA_BIN"])
	}
	unlock, err := r.generationLock(ctx)
	if err != nil {
		return p, pin, err
	}
	defer unlock()
	var gen harnessmgr.ConfigGeneration
	if pin.Generation != "" {
		gen, err = loadNativeGeneration(a.Harness, pin.Generation, lookup)
		if err == nil && !pinnedGenerationUsable(a.Harness, pin.Generation, gen, home, privateEnv["DYNA_BIN"]) || (a.Harness == "pi" && pinnedPiCompactionDirty(pin.Generation)) {
			// TODO: legacy/shim remove later — rebuild older pins without private HOME/XDG roots, the current native feature policy or a Dyna command that still runs, and pins whose settings still carry the compaction overrides Dyna once wrote.
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
			env[k] = v
		}
	}
	// Apply the scope last, including when a resumed generation has old paths.
	for k, v := range privateEnv {
		env[k] = v
	}
	applyNativeFeatureEnvironment(a.Harness, env)
	// A harness that keeps credentials per account reads them from that
	// account's own home. Pi reads settings, MCP servers and models through
	// per-process flags from the generation, so its account home holds only the
	// login, and session history stays in the shared harness home.
	if a.Harness == "pi" {
		env[piAgentDirKey] = active.Home
	}
	if pinsCredentials(a.Harness) {
		env[pinnedSecretKey] = active.Home
	}
	if err := validateAccountManagerEnvAt(a.Harness, home, active.Home, env); err != nil {
		return p, pin, err
	}
	env["PATH"] = filepath.Dir(b.Executable) + string(os.PathListSeparator) + privateEnv["PATH"]
	p.RuntimeExecutable = b.Executable
	p.Env = env
	p.RuntimePermissionArgs = append([]string{}, p.ExtraArgs...)
	p.ExtraArgs = append(append([]string{}, p.ExtraArgs...), gen.Args...)
	// The owner's shared model definitions join the invocation, not the
	// generation: a generation keeps its saved argv while its inputs do not
	// change, so a models.json that appears or disappears later must not be
	// frozen into it.
	if a.Harness == "pi" {
		models := filepath.Join(home, "models.json")
		if _, err := os.Stat(models); err == nil {
			p.ExtraArgs = append(p.ExtraArgs, "--models-file", models)
		}
	}
	if leaseOut != nil {
		lease, err := r.leaseGenerationLocked(gen.Directory)
		if err != nil {
			return p, pin, err
		}
		*leaseOut = lease
	}
	return p, pin, nil
}

// pinnedGenerationUsable reports whether a resumed session may keep this
// generation. A pin whose Dyna MCP command no longer runs cannot give the next
// turn any Dyna tools, so the turn rebuilds it instead. cli is the Dyna command
// this turn publishes: a pin that names another one would keep calling the
// build the session first started on, so it rebuilds too.
func pinnedGenerationUsable(harness, dir string, gen harnessmgr.ConfigGeneration, nativeHome, cli string) bool {
	if !privateGenerationRoots(gen.Env, nativeHome) || !currentNativeFeaturePolicy(dir, harness) {
		return false
	}
	if gen.CLI != "" && cli != "" && gen.CLI != cli {
		return false
	}
	if nativeFeaturePolicies[harness].DynaShell {
		return harnesshome.Launches(gen.CLI)
	}
	return true
}

// pinnedPiCompactionDirty reports whether the pinned Pi settings still carry
// the per-model compaction overrides an older Dyna generation wrote. Dyna
// passes the trigger per invocation now, so it never writes compaction into a
// generation; republishing such a pin would push its stale reserves onto every
// session on the account and cap the invocation trigger below its target.
// A missing settings file or an owner's own compaction settings that carry no
// reserve override leaves the pin alone.
// TODO: legacy/shim remove later — covers pins created before the trigger flag.
func pinnedPiCompactionDirty(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return false
	}
	var doc struct {
		Compaction struct {
			ModelOverrides map[string]struct {
				ReserveTokens *int `json:"reserveTokens"`
			} `json:"modelOverrides"`
		} `json:"compaction"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for _, entry := range doc.Compaction.ModelOverrides {
		if entry.ReserveTokens != nil {
			return true
		}
	}
	return false
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
		"systemd-run also bridges to the owner binary: with DYNA_ROOM_SLICE set, its --user calls without a slice of their own join the room's slice.",
	}
	switch harness {
	case "claude-code":
		warnings = append(warnings, "A copied login shares its refresh-token family with the personal login. A refresh on either side can require a new sign-in on the other side. Dyna does not repeat the import.")
	}
	return warnings
}
func (r *Runtime) observeAccount(ctx context.Context, id, exe string) (NativeAccount, error) {
	n, err := r.Accounts.Find(id)
	if err != nil {
		return n, err
	}
	m, err := r.Accounts.launchManager(id, exe)
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
	selections, _ := (&accountstore.Store{Root: r.Accounts.Root}).Selections()
	for _, a := range RuntimeAdapters() {
		if harness != "" && canonicalRuntimeID(harness) != a.Harness {
			continue
		}
		for _, selection := range selections {
			if selection.Harness != a.Harness || selection.Scope != "" || selection.Account == "" {
				continue
			}
			active, err := r.Accounts.Find(selection.Account)
			if err != nil {
				continue
			}
			if _, err := r.regenerateAccount(active); err != nil {
				failures = append(failures, a.Harness)
			}
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

// EnsureCatalog removes skill material that older Dyna versions wrote into
// harness homes and beside the catalog. Skills are a global catalog now, so no
// home should hold a Dyna skill link. The cleanup is idempotent: it repeats on
// every generation until an old home is gone, and it leaves a real directory or
// file that the owner or a harness owns.
func (r *Runtime) EnsureCatalog() error {
	return r.removeLegacySkillInstalls()
}

// OpenPins lists the bundles held in use by settings and open sessions, with
// a holder name for each: the profile that selects the version, or the
// session whose runtime pin holds it.
func OpenPins() ([]PinnedBundle, error) {
	var selected struct {
		Profiles []struct {
			Name    string `json:"name"`
			Harness string `json:"harness"`
			Version string `json:"runtimeVersion"`
		} `json:"profiles"`
	}
	if err := store.ReadJSON(profile.DefaultPath(), &selected); err != nil {
		return nil, errors.New("Selected runtime versions could not be read; cleanup stopped.")
	}
	out := []PinnedBundle{}
	for _, p := range selected.Profiles {
		if p.Version == "" {
			continue
		}
		h, err := CanonicalHarness(p.Harness)
		if err != nil || !validVersion(p.Version) {
			return nil, errors.New("A selected private runtime version is invalid; cleanup stopped.")
		}
		holder := "A selected profile"
		if p.Name != "" {
			holder = "Profile " + p.Name
		}
		out = append(out, PinnedBundle{Bundle: Bundle{Harness: h, Version: p.Version}, Holder: holder})
	}
	root := filepath.Join(store.DataDir(), "sessions")
	err := sessionlife.Each(root, true, func(id string, raw []byte, err error) error {
		var meta struct {
			sessionlife.Snapshot
			RuntimePin *Pin `json:"runtimePin"`
		}
		if err != nil || json.Unmarshal(raw, &meta) != nil {
			return errors.New("session pins could not be read; bundle cleanup stopped")
		}
		if !meta.IsClosed() && meta.RuntimePin != nil {
			holder := "Session " + id
			if meta.ID != "" {
				holder = "Session " + meta.ID
			}
			out = append(out, PinnedBundle{Bundle: meta.RuntimePin.Bundle, Holder: holder})
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
	active, err := NewAccounts().Active(SelectionProvider(n.Harness, n.Provider), n.Harness)
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
	Concurrent          bool   `json:"concurrent"`
	SharedPersonalLogin bool   `json:"sharedPersonalLogin"`
}

func Capabilities() []Capability {
	out := []Capability{}
	for _, a := range RuntimeAdapters() {
		c := Capability{Harness: a.Harness, Status: "ready", Reason: strings.Join(a.PartialReasons, " "), InstallSupported: a.NPMPackage != "" || a.Install != nil, AccountBlocked: a.AccountBlocked, MCP: true, Concurrent: !a.Exclusive}
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
	return errors.Is(err, native.ErrNotSignedIn) || errors.Is(err, native.ErrInvalidGrant)
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
