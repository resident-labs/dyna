// Package profile manages worker profiles: registered model+harness combos
// with human-authored descriptions and standardized stats (taste,
// diagnostics, intelligence, cost; all 1..10, higher is better; for cost,
// higher means cheaper / better value).
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"dyna-agent/internal/workeroutput"
)

// Harness identifies which CLI executes the worker.
const (
	HarnessClaudeCode  = "claude-code"
	HarnessCodex       = "codex"
	HarnessAntigravity = "antigravity"
	HarnessOpenCode    = "opencode"
	HarnessMuse        = "muse"
	HarnessGrok        = "grok"
	HarnessPi          = "pi"
	HarnessCustom      = "custom"
	HarnessMock        = "mock"
	AntigravityModel   = "gemini-3.8-flash-high"
)

var Harnesses = harnessNames()

// EffectiveDialect resolves a declaration or the default for programmatic profiles.
func (p Profile) EffectiveDialect() string {
	if p.Output != nil && strings.TrimSpace(p.Output.Dialect) != "" {
		return strings.TrimSpace(p.Output.Dialect)
	}
	h, _ := HarnessDefinitionFor(p.Harness)
	return h.Dialect
}

// EffectiveOutput returns a fresh declaration for runtime readers and snapshots.
func (p Profile) EffectiveOutput() *workeroutput.Declaration {
	return &workeroutput.Declaration{Dialect: p.EffectiveDialect()}
}

// Profile describes one registered worker.
type Profile struct {
	Name              string   `json:"name"`
	RuntimeVersion    string   `json:"runtimeVersion,omitempty"`
	RuntimeSkillPaths []string `json:"-"`
	// RuntimeRepo is the repository identity of the turn, set by the runner.
	RuntimeRepo string `json:"-"`
	// RuntimePermissionArgs snapshots user arguments before generated tool denials.
	// A non-nil empty slice identifies a prepared default profile.
	RuntimePermissionArgs []string `json:"-"`
	// RuntimeInstructions is the run's instructions text (rules, worker
	// context). Native channels use it on each invocation. Prompt channels
	// send it on a new snapshot and after controlled compaction.
	RuntimeInstructions string                      `json:"-"`
	RuntimeBindProcess  func(json.RawMessage) error `json:"-"`
	RuntimeExecutable   string                      `json:"-"`
	Description         string                      `json:"description"`
	Harness             string                      `json:"harness"`
	Models              []string                    `json:"models,omitempty"`
	Model               string                      `json:"model,omitempty"`
	// Fallback names the profile that continues a session when a turn hits a
	// usage limit and no account on this harness can take it. The named
	// profile's own fallback continues the chain, so chains stay a few hops.
	Fallback     string                    `json:"fallback,omitempty"`
	Output       *workeroutput.Declaration `json:"output,omitempty"`
	Taste        int                       `json:"taste"`        // 1..10, higher better
	Intelligence int                       `json:"intelligence"` // 1..10, higher better
	Cost         int                       `json:"cost"`         // 1..10, higher better (10 = very cheap)
	Diagnostics  int                       `json:"diagnostics"`  // 1..10, higher better; 0 (unset) ranks as 5
	ExtraArgs    []string                  `json:"extraArgs,omitempty"`
	// Compact sets the harness's native auto-compact threshold; nil keeps its default.
	Compact    *Compact          `json:"compact,omitempty"`
	Command    []string          `json:"command,omitempty"` // custom harness: argv, {{prompt}}/{{model}} placeholders
	Env        map[string]string `json:"env,omitempty"`
	TimeoutSec int               `json:"timeoutSec,omitempty"`
	Default    bool              `json:"default,omitempty"`
	// Managed is retained for stores created by older dyna versions and as a
	// user-controlled marker. It has no automatic update behavior.
	// TODO: legacy/shim remove later — reads the old managed profile marker; remove after stored profiles and marker users migrate
	Managed bool `json:"managed,omitempty"`
	// SafeMode keeps the harness's own permission prompts / sandbox. By
	// default dyna bypasses them (workers run headless and must act freely).
	SafeMode bool `json:"safeMode,omitempty"`
	// Uses lists who may run this profile: UseWorkflows, UseAgents and
	// UseServices, in that order. Empty means all three. See Allows.
	Uses []string `json:"uses,omitempty"`
	// MaxConcurrent caps how many workers of this profile may run at once
	// across a workflow (0 = unlimited). Expensive models set this low.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	// MaxCallsPerRun caps total agent() calls to this profile in one run
	// (0 = unlimited). Exceeding it fails the run after accepted calls drain.
	MaxCallsPerRun int `json:"maxCallsPerRun,omitempty"`
	// Disabled hides the profile from workflows (agent() calls to it fail,
	// it leaves the scripts' profiles global) while keeping its stats and
	// description saved and editable.
	Disabled bool `json:"disabled,omitempty"`
}

// EffectiveDiagnostics returns the diagnostics rating used for ranking and
// display.
func (p Profile) EffectiveDiagnostics() int {
	if p.Diagnostics == 0 {
		return 5
	}
	return p.Diagnostics
}

// NormalizeModels cleans a model declaration and selects its primary model.
// A multi-model list takes precedence. A scalar override replaces a single entry.
// Legacy scalar-only declarations keep a nil list.
func NormalizeModels(model string, models []string) (string, []string) {
	model = strings.TrimSpace(model)
	var cleaned []string
	seen := make(map[string]bool, len(models))
	for _, candidate := range models {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			cleaned = append(cleaned, candidate)
		}
	}
	if len(cleaned) == 0 {
		return model, nil
	}
	if len(cleaned) == 1 && model != "" && model != cleaned[0] {
		cleaned[0] = model
	}
	return cleaned[0], cleaned
}

// NormalizeModels updates the profile's model declaration in place.
func (p *Profile) NormalizeModels() {
	p.Model, p.Models = NormalizeModels(p.Model, p.Models)
}

// EffectiveModels returns a fresh, ordered list of models for execution.
func (p Profile) EffectiveModels() []string {
	model, models := NormalizeModels(p.Model, p.Models)
	if len(models) > 0 {
		return models
	}
	if model != "" {
		return []string{model}
	}
	return nil
}

// DynaManaged reports whether Dyna runs this profile on its private harness copy.
// Custom and mock harnesses run their own command.
func (p Profile) DynaManaged() bool {
	def, ok := HarnessDefinitionFor(p.Harness)
	return ok && def.Binary != ""
}

func (p *Profile) Validate() error {
	p.NormalizeModels()
	if p.RuntimeVersion != "" && !p.DynaManaged() {
		return errors.New("runtimeVersion requires a catalog harness")
	}

	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	if p.Uses != nil && len(p.Uses) == 0 {
		return errors.New("choose at least one use: workflows, agents or services")
	}
	var uses []string
	for _, u := range p.Uses {
		if !slices.Contains(AllUses, u) {
			return fmt.Errorf("uses must be among %s, not %q", strings.Join(AllUses, ", "), u)
		}
	}
	for _, u := range AllUses {
		if slices.Contains(p.Uses, u) {
			uses = append(uses, u)
		}
	}
	if len(uses) == len(AllUses) {
		uses = nil
	}
	p.Uses = uses
	if strings.ContainsAny(p.Name, " \t\n") {
		return errors.New("profile name must not contain whitespace (use dashes)")
	}
	if p.Fallback != "" {
		if strings.ContainsAny(p.Fallback, " \t\n") || len(p.Fallback) > 128 {
			return errors.New("fallback must be a profile name without spaces")
		}
		if p.Fallback == p.Name {
			return errors.New("a profile cannot fall back to itself")
		}
	}
	ok := false
	for _, h := range Harnesses {
		if p.Harness == h {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("unknown harness %q (valid: %s)", p.Harness, strings.Join(Harnesses, ", "))
	}
	for _, v := range []struct {
		n string
		v int
	}{{"taste", p.Taste}, {"intelligence", p.Intelligence}, {"cost", p.Cost}} {
		if v.v < 1 || v.v > 10 {
			return fmt.Errorf("%s must be 1..10 (got %d)", v.n, v.v)
		}
	}
	// Zero is allowed: profiles saved before this rating existed store 0.
	if p.Diagnostics != 0 && (p.Diagnostics < 1 || p.Diagnostics > 10) {
		return fmt.Errorf("diagnostics must be 1..10 (got %d)", p.Diagnostics)
	}
	if p.Harness == HarnessCustom && len(p.Command) == 0 {
		return errors.New("custom harness requires a command")
	}
	if err := validateCompact(*p); err != nil {
		return err
	}
	if p.Harness == HarnessAntigravity {
		for _, arg := range p.ExtraArgs {
			if arg == "--model" || strings.HasPrefix(arg, "--model=") {
				return errors.New("antigravity profile extraArgs may not override --model")
			}
		}
	}
	if p.Output != nil && strings.TrimSpace(p.Output.Dialect) != "" {
		if err := workeroutput.ValidateDeclaration(p.Output); err != nil {
			return err
		}
		definition, _ := HarnessDefinitionFor(p.Harness)
		if p.Harness != HarnessCustom && p.EffectiveDialect() != definition.Dialect {
			return fmt.Errorf("harness %q emits %q, so output dialect %q cannot decode it; omit output.dialect to use the native format (use custom for another format)", p.Harness, definition.Dialect, p.EffectiveDialect())
		}
	}
	for _, v := range []struct {
		n string
		v int
	}{
		{"timeoutSec", p.TimeoutSec},
		{"maxConcurrent", p.MaxConcurrent},
		{"maxCallsPerRun", p.MaxCallsPerRun},
	} {
		if v.v < 0 {
			return fmt.Errorf("%s must be non-negative (got %d)", v.n, v.v)
		}
	}
	return nil
}

// Store is the on-disk profile registry.
type Store struct {
	Path     string    `json:"-"`
	Version  int       `json:"version"` // 3 = explicit output declarations
	Profiles []Profile `json:"profiles"`
}

const storeVersion = 3

func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna", "profiles.json")
}

func Load(path string) (*Store, error) {
	s := &Store{Path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s.Path = path
	for i := range s.Profiles {
		p := &s.Profiles[i]
		migrateAntigravityACP(p)
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("invalid profile %q in %s: %w", p.Name, path, err)
		}
	}
	sort.Slice(s.Profiles, func(i, j int) bool { return s.Profiles[i].Name < s.Profiles[j].Name })
	return s, nil
}

// TODO: legacy/shim remove later — loads Antigravity profiles saved before the ACP harness (antigravity-stream-json dialect, --effort arguments); remove once stores saved by earlier releases are gone.
// ACP emits acp-json and takes the effort from the model, so both old values
// would make the whole store fail to load.
func migrateAntigravityACP(p *Profile) {
	if p.Harness != HarnessAntigravity {
		return
	}
	if p.Output != nil && p.Output.Dialect == workeroutput.DialectAntigravityStreamJSON {
		p.Output.Dialect = workeroutput.DialectAcpJSON
	}
	args := p.ExtraArgs[:0:0]
	for i := 0; i < len(p.ExtraArgs); i++ {
		arg := p.ExtraArgs[i]
		if arg == "--effort" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--effort=") {
			continue
		}
		args = append(args, arg)
	}
	if len(args) == 0 {
		args = nil
	}
	p.ExtraArgs = args
}

func cloneProfile(p Profile) Profile {
	p.Models = slices.Clone(p.Models)
	p.Output = workeroutput.CloneDeclaration(p.Output)
	p.ExtraArgs = slices.Clone(p.ExtraArgs)
	p.RuntimePermissionArgs = slices.Clone(p.RuntimePermissionArgs)
	p.Compact = cloneCompact(p.Compact)
	p.Command = slices.Clone(p.Command)
	p.Env = maps.Clone(p.Env)
	return p
}

func cloneCompact(c *Compact) *Compact {
	if c == nil {
		return nil
	}
	v := *c
	v.Windows = maps.Clone(c.Windows)
	return &v
}

func (s *Store) Save() error {
	if s.Version < storeVersion {
		s.Version = storeVersion
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, append(b, '\n'), 0o644)
}

func (s *Store) Get(name string) (*Profile, bool) {
	for i := range s.Profiles {
		if s.Profiles[i].Name == name {
			return &s.Profiles[i], true
		}
	}
	return nil, false
}

func (s *Store) Upsert(p Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	// A saved fallback must name a registered profile. Load does not check
	// this, so removing a fallback target never makes the store unreadable.
	if p.Fallback != "" {
		if _, ok := s.Get(p.Fallback); !ok {
			return fmt.Errorf("fallback profile %q is not registered", p.Fallback)
		}
	}
	if p.Default {
		for i := range s.Profiles {
			s.Profiles[i].Default = false
		}
	}
	for i := range s.Profiles {
		if s.Profiles[i].Name == p.Name {
			s.Profiles[i] = p
			return nil
		}
	}
	s.Profiles = append(s.Profiles, p)
	sort.Slice(s.Profiles, func(i, j int) bool { return s.Profiles[i].Name < s.Profiles[j].Name })
	return nil
}

func (s *Store) Remove(name string) bool {
	for i := range s.Profiles {
		if s.Profiles[i].Name == name {
			s.Profiles = append(s.Profiles[:i], s.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

// Profile uses. Each one is a switch: UseWorkflows lets workflow agent()
// calls run the profile, UseAgents lets dyna subagent sessions (and the
// internal root) run it, and UseServices lets Dyna's own automatic workers
// run it: PR reviewers and their helpers.
const (
	UseWorkflows = "workflows"
	UseAgents    = "agents"
	UseServices  = "services"
)

// AllUses lists the profile uses in display order.
var AllUses = []string{UseWorkflows, UseAgents, UseServices}

// EffectiveUses lists the uses the profile allows, all three when none is set.
func (p *Profile) EffectiveUses() []string {
	if len(p.Uses) == 0 {
		return slices.Clone(AllUses)
	}
	return slices.Clone(p.Uses)
}

// Allows reports whether an enabled profile serves use.
func (p *Profile) Allows(use string) bool {
	return !p.Disabled && slices.Contains(p.EffectiveUses(), use)
}

// WorkflowEligible reports whether a workflow may run this profile through agent().
func (p *Profile) WorkflowEligible() bool { return p.Allows(UseWorkflows) }

// ForSubagents reports whether a dyna subagent session, including the
// internal root, may run this profile.
func (p *Profile) ForSubagents() bool { return p.Allows(UseAgents) }

// ForServices reports whether one of Dyna's services may run this profile.
func (p *Profile) ForServices() bool { return p.Allows(UseServices) }

// DefaultProfile returns the enabled profile marked default, or the first
// enabled one, whatever its use. Callers that start work use the default
// for their kind instead.
func (s *Store) DefaultProfile() (*Profile, bool) {
	return s.defaultProfile(func(p *Profile) bool { return !p.Disabled })
}

// SubagentDefaultProfile returns the default for dyna subagent sessions. It
// skips services-only profiles, even one marked default.
func (s *Store) SubagentDefaultProfile() (*Profile, bool) {
	return s.defaultProfile((*Profile).ForSubagents)
}

// WorkflowDefaultProfile returns the default for workflow agent() calls.
// It skips profiles not for all uses, even one marked default.
func (s *Store) WorkflowDefaultProfile() (*Profile, bool) {
	return s.defaultProfile((*Profile).WorkflowEligible)
}

// WorkflowProfile resolves a registered profile using agent()'s selection policy.
func (s *Store) WorkflowProfile(name string) (*Profile, error) {
	if name == "" {
		p, ok := s.WorkflowDefaultProfile()
		if !ok {
			return nil, fmt.Errorf("no profiles registered; run `dyna profiles add` first")
		}
		return p, nil
	}
	p, ok := s.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown profile %q; run `dyna profiles list` to see registered profiles", name)
	}
	if p.Disabled {
		return nil, fmt.Errorf("profile %q is disabled; the user must enable it (`dyna profiles enable %s`); pick another from the profiles global", name, name)
	}
	if !p.WorkflowEligible() {
		return nil, fmt.Errorf("profile %q is not enabled for workflows (uses: %s); workflows cannot use it with agent(); pick another from the profiles global", name, strings.Join(p.EffectiveUses(), ", "))
	}
	return p, nil
}

// ServiceDefaultProfile returns the default for Dyna's services. It skips
// subagents-only profiles, even one marked default.
func (s *Store) ServiceDefaultProfile() (*Profile, bool) {
	return s.defaultProfile((*Profile).ForServices)
}

func (s *Store) defaultProfile(eligible func(*Profile) bool) (*Profile, bool) {
	for i := range s.Profiles {
		if s.Profiles[i].Default && eligible(&s.Profiles[i]) {
			return &s.Profiles[i], true
		}
	}
	for i := range s.Profiles {
		if eligible(&s.Profiles[i]) {
			return &s.Profiles[i], true
		}
	}
	return nil, false
}
