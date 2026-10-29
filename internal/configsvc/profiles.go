package configsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// ProfileView is a profile as an editor shows it. Env values never leave
// Go: they often hold API keys. The editor sees only which keys are set.
//
// ExtraArgs and Command do reach the editor, and they can carry a token
// (`--api-key ...`). So the whole configsvc API is for the owner only: a
// web server must put every config call behind its token, with no
// exceptions. roomsvc.ProfileChoice is the only profile shape that is safe
// to serve without one.
type ProfileView struct {
	RuntimeVersion string   `json:"runtimeVersion"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Harness        string   `json:"harness"`
	Model          string   `json:"model"`
	Models         []string `json:"models,omitempty"`
	// Fallback names the profile that continues a session when this profile's
	// harness hits a usage limit with no account left. The next profile's own
	// fallback continues the chain.
	Fallback       string   `json:"fallback"`
	Taste          int      `json:"taste"`
	Intelligence   int      `json:"intelligence"`
	Cost           int      `json:"cost"`
	Diagnostics    int      `json:"diagnostics"`
	TimeoutSec     int      `json:"timeoutSec"`
	MaxConcurrent  int      `json:"maxConcurrent"`
	MaxCallsPerRun int      `json:"maxCallsPerRun"`
	Default        bool     `json:"default"`
	Disabled       bool     `json:"disabled"`
	Uses           []string `json:"uses"`
	SafeMode       bool     `json:"safeMode"`
	ExtraArgs      []string `json:"extraArgs"`
	// Compact is the auto-compact threshold; null keeps the harness default.
	Compact *profile.Compact `json:"compact"`
	EnvKeys []string         `json:"envKeys"`
	// Support is how much of the harness's output Dyna reads. It is derived
	// from the profile's output dialect and ignored on save.
	Support HarnessSupport `json:"support"`
	// Problem is the typed reason this profile cannot run, so the editor can
	// offer repair instead of refusing the whole list. Empty means launchable.
	Problem profile.Status `json:"problem,omitempty"`
}

// ProfileList is every profile plus the harnesses a profile may use.
type ProfileList struct {
	Profiles  []ProfileView `json:"profiles"`
	Harnesses []string      `json:"harnesses"`
	// Unready maps each harness a new profile cannot use yet to the reason.
	Unready map[string]string `json:"unready"`
	// Default is the profile a session starts with when none is named.
	Default string `json:"default"`
}

// ProfileEdit saves one profile. Original names the profile being edited;
// empty creates one. A different Name renames it. Env changes are explicit:
// EnvSet adds or replaces values, EnvRemove drops keys, and every other
// stored key keeps its value unseen.
type ProfileEdit struct {
	Original  string            `json:"original"`
	Profile   ProfileView       `json:"profile"`
	EnvSet    map[string]string `json:"envSet"`
	EnvRemove []string          `json:"envRemove"`
}

// loadProfiles admits the registry for execution: every profile must be
// launchable. Management paths use loadProfilesForRepair, which validates each
// profile separately.
func loadProfiles() (*profile.Store, error) { return profile.Load(profile.DefaultPath()) }

// loadProfilesForRepair is the one management read: it lists every profile with
// its typed validation status, so an owner can remove or replace one that names
// a harness Dyna has retired.
func loadProfilesForRepair() (profile.Registry, error) {
	return profile.ReadConfig(profile.DefaultPath())
}

func view(p profile.Profile) ProfileView {
	v := ProfileView{
		RuntimeVersion: p.RuntimeVersion,
		Name:           p.Name, Description: p.Description, Harness: p.Harness, Model: p.Model, Models: p.EffectiveModels(),
		Fallback: p.Fallback,
		Taste:    p.Taste, Diagnostics: p.EffectiveDiagnostics(), Intelligence: p.Intelligence, Cost: p.Cost,
		TimeoutSec: p.TimeoutSec, MaxConcurrent: p.MaxConcurrent, MaxCallsPerRun: p.MaxCallsPerRun,
		Default: p.Default, Disabled: p.Disabled, Uses: p.EffectiveUses(), SafeMode: p.SafeMode,
		ExtraArgs: append([]string{}, p.ExtraArgs...), Compact: p.Compact, EnvKeys: []string{},
	}
	for k := range p.Env {
		v.EnvKeys = append(v.EnvKeys, k)
	}
	sort.Strings(v.EnvKeys)
	v.Support = supportFor(p)
	return v
}

// Profiles lists every profile, disabled ones included, in registry order.
func Profiles() (ProfileList, error) {
	reg, err := loadProfilesForRepair()
	if err != nil {
		return ProfileList{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out := ProfileList{Profiles: []ProfileView{}, Harnesses: profile.Harnesses, Unready: harnessReadiness(ctx)}
	for _, p := range reg.Store.Profiles {
		v := view(p)
		v.Problem = reg.Reports[p.Name].Status
		out.Profiles = append(out.Profiles, v)
	}
	if d, ok := reg.Store.DefaultProfile(); ok {
		out.Default = d.Name
	}
	return out, nil
}

// RequireHarness rejects a harness that Dyna has not installed and cannot
// install on first use now. Custom and mock harnesses need no private copy.
func RequireHarness(ctx context.Context, p profile.Profile) error {
	if !p.DynaManaged() {
		return nil
	}
	_, err := health.HarnessReady(ctx, p.Harness, p.RuntimeVersion)
	return err
}

// HarnessReadiness maps each catalog harness that Dyna has not installed and
// cannot install now to the reason. The profile wizard disables those.
func harnessReadiness(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, h := range profile.Harnesses {
		if err := RequireHarness(ctx, profile.Profile{Harness: h}); err != nil {
			out[h] = err.Error()
		}
	}
	return out
}

// SaveProfile creates, edits or renames a profile through profile.Store,
// the same validation `dyna profiles add` uses. Fields the editor does not
// show (output declaration, managed flag) are kept. It notes the change on
// s.Sessions, or on a fresh Manager when that is nil (the CLI).
func (s *Service) SaveProfile(ctx context.Context, e ProfileEdit) (ProfileView, error) {
	if err := authority.Check("profiles add", authority.Of(ctx), nil); err != nil {
		return ProfileView{}, err
	}
	reg, err := loadProfilesForRepair()
	if err != nil {
		return ProfileView{}, err
	}
	in := e.Profile
	in.Name = strings.TrimSpace(in.Name)
	var p profile.Profile
	if e.Original != "" {
		old, ok := reg.Store.Get(e.Original)
		if !ok {
			return ProfileView{}, fmt.Errorf("no profile is named %q", e.Original)
		}
		p = *old
	}
	if in.Name != e.Original {
		if _, taken := reg.Store.Get(in.Name); taken {
			return ProfileView{}, fmt.Errorf("a profile named %q already exists", in.Name)
		}
	}
	if e.Original == "" || p.Harness != in.Harness {
		if err := RequireHarness(ctx, profile.Profile{Harness: in.Harness, RuntimeVersion: in.RuntimeVersion}); err != nil {
			return ProfileView{}, err
		}
	}
	// A harness change also changes its native stream. The editor has no
	// dialect field, so discard the old harness's declaration and use the new
	// default instead of retaining a declaration that validation must reject.
	if p.Harness != in.Harness {
		p.Output = nil
	}
	p.RuntimeVersion = in.RuntimeVersion
	p.Name, p.Description, p.Harness, p.Model = in.Name, strings.TrimSpace(in.Description), in.Harness, strings.TrimSpace(in.Model)
	p.Model, p.Models = profile.NormalizeModels(in.Model, in.Models)
	if len(p.Models) <= 1 {
		p.Models = nil
	}
	p.Fallback = strings.TrimSpace(in.Fallback)
	p.Taste, p.Intelligence, p.Cost = in.Taste, in.Intelligence, in.Cost
	p.Diagnostics = in.Diagnostics
	p.TimeoutSec, p.MaxConcurrent, p.MaxCallsPerRun = in.TimeoutSec, in.MaxConcurrent, in.MaxCallsPerRun
	p.Default, p.Disabled, p.Uses, p.SafeMode = in.Default, in.Disabled, in.Uses, in.SafeMode
	p.ExtraArgs, p.Compact = clean(in.ExtraArgs), in.Compact
	env := map[string]string{}
	for k, v := range p.Env {
		env[k] = v
	}
	for _, k := range e.EnvRemove {
		delete(env, k)
	}
	for k, v := range e.EnvSet {
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, "= \t\n") {
			return ProfileView{}, fmt.Errorf("env key %q must be a name without spaces or '='", k)
		}
		env[k] = v
	}
	p.Env = nil
	if len(env) > 0 {
		p.Env = env
	}
	if err := saveProfile(ctx, s.Sessions, reg.Store, p, e.Original); err != nil {
		return ProfileView{}, err
	}
	return view(p), nil
}

func clean(list []string) []string {
	var out []string
	for _, a := range list {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// RemoveProfile deletes a profile. Sessions that already use it keep their
// copy of its settings.
func (s *Service) RemoveProfile(ctx context.Context, name string) error {
	if err := authority.Check("profiles remove", authority.Of(ctx), nil); err != nil {
		return err
	}
	reg, err := loadProfilesForRepair()
	if err != nil {
		return err
	}
	if !reg.Store.Remove(name) {
		return fmt.Errorf("no profile is named %q", name)
	}
	return saveProfiles(ctx, s.Sessions, reg.Store)
}

// SetDefaultProfile makes name the default, like `dyna profiles set-default`.
func (s *Service) SetDefaultProfile(ctx context.Context, name string) error {
	if err := authority.Check("profiles set-default", authority.Of(ctx), nil); err != nil {
		return err
	}
	return update(ctx, s.Sessions, name, func(p *profile.Profile) error {
		if p.Disabled {
			return errors.New("enable the profile before you make it the default")
		}
		p.Default = true
		return nil
	})
}

// SetProfileDisabled enables or disables a profile, like `dyna profiles
// enable` and `disable`.
func (s *Service) SetProfileDisabled(ctx context.Context, name string, disabled bool) error {
	action := "profiles enable"
	if disabled {
		action = "profiles disable"
	}
	if err := authority.Check(action, authority.Of(ctx), nil); err != nil {
		return err
	}
	return update(ctx, s.Sessions, name, func(p *profile.Profile) error {
		p.Disabled = disabled
		return nil
	})
}

func update(ctx context.Context, sessions *subagent.Manager, name string, fn func(*profile.Profile) error) error {
	reg, err := loadProfilesForRepair()
	if err != nil {
		return err
	}
	old, ok := reg.Store.Get(name)
	if !ok {
		return fmt.Errorf("no profile is named %q", name)
	}
	p := *old
	if err := fn(&p); err != nil {
		return err
	}
	return saveProfile(ctx, sessions, reg.Store, p, "")
}

// saveProfile shares validation, rename and persistence for both input shapes.
func saveProfile(ctx context.Context, sessions *subagent.Manager, reg *profile.Store, p profile.Profile, original string) error {
	if err := reg.Upsert(p); err != nil {
		return err
	}
	if original != "" && original != p.Name {
		reg.Remove(original)
	}
	return saveProfiles(ctx, sessions, reg)
}

// afterReply runs a profile write's instruction fan-out after the reply. Tests
// replace it to run the fan-out on their own schedule.
var afterReply = func(fn func()) { go fn() }

// saveProfiles notes the change on sessions, the engine's Manager, or on a
// fresh one when it is nil. The engine's note runs after the reply, because
// it can fan out to about 40 sessions and outlast the CLI's deadline. A nil
// Manager belongs to a CLI process that exits on return, so its note stays
// synchronous.
func saveProfiles(ctx context.Context, sessions *subagent.Manager, s *profile.Store) error {
	if err := s.Save(); err != nil {
		return err
	}
	if sessions == nil {
		subagent.NewManager().NoteRoomInstructions(ctx, "")
		return nil
	}
	detached := context.WithoutCancel(ctx)
	afterReply(func() { sessions.NoteRoomInstructions(detached, "") })
	return nil
}

// UpsertProfile keeps the complete CLI profile, including its output declaration
// and custom command. The CLI replaces all stored fields.
func (s *Service) UpsertProfile(ctx context.Context, p profile.Profile) error {
	if err := authority.Check("profiles add", authority.Of(ctx), nil); err != nil {
		return err
	}
	// Repair path: replacing a profile that names a retired harness must work,
	// and the replacement itself is validated by Upsert below.
	reg, err := loadProfilesForRepair()
	if err != nil {
		return err
	}
	if old, ok := reg.Store.Get(p.Name); !ok || old.Harness != p.Harness {
		if err := RequireHarness(ctx, p); err != nil {
			return err
		}
	}
	return saveProfile(ctx, s.Sessions, reg.Store, p, "")
}

// SetProfileUses changes only the use restrictions and keeps all other fields.
func (s *Service) SetProfileUses(ctx context.Context, name string, uses []string) ([]string, error) {
	if err := authority.Check("profiles set", authority.Of(ctx), nil); err != nil {
		return nil, err
	}
	reg, err := loadProfilesForRepair()
	if err != nil {
		return nil, err
	}
	old, ok := reg.Store.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	p := *old
	p.Uses = uses
	if err := saveProfile(ctx, s.Sessions, reg.Store, p, ""); err != nil {
		return nil, err
	}
	saved, _ := reg.Store.Get(name)
	return saved.EffectiveUses(), nil
}
