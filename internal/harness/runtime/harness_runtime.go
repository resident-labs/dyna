package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
)

type PrivateRuntimeSnapshot struct {
	Bundles             []Bundle             `json:"bundles"`
	Accounts            []NativeAccount      `json:"accounts"`
	Profiles            []Doctor             `json:"profiles"`
	Capabilities        []Capability         `json:"capabilities"`
	SelectedVersions    map[string]string    `json:"selectedVersions"`
	RecommendedVersions []RecommendedVersion `json:"recommendedVersions"`
	// Installing lists harnesses whose CLI is being installed on first use.
	Installing []string `json:"installing"`
}

// RefreshPrivateRuntimes checks vendor metadata now, then returns the snapshot.
func (a *Runtime) RefreshPrivateRuntimes(ctx context.Context) (PrivateRuntimeSnapshot, error) {
	return a.privateRuntimes(ctx, true)
}

func (a *Runtime) PrivateRuntimes(ctx context.Context) (PrivateRuntimeSnapshot, error) {
	return a.privateRuntimes(ctx, false)
}

func (a *Runtime) privateRuntimes(ctx context.Context, refresh bool) (PrivateRuntimeSnapshot, error) {
	out := PrivateRuntimeSnapshot{Profiles: []Doctor{}, Capabilities: Capabilities(), SelectedVersions: map[string]string{}, Installing: []string{}}
	firstUse := NewFirstUse()
	var err error
	if out.Bundles, err = a.Bundles.List(); err != nil {
		return out, err
	}
	for _, adapter := range RuntimeAdapters() {
		if firstUse.Installing(adapter.Harness) {
			out.Installing = append(out.Installing, adapter.Harness)
		}
		selected, _ := a.Bundles.SelectedVersion(adapter.Harness)
		for _, b := range out.Bundles {
			if b.Harness == adapter.Harness && (selected == "" || selected == b.Version) {
				out.SelectedVersions[adapter.Harness] = b.Version
				break
			}
		}
	}
	if out.Accounts, err = a.Accounts.List(); err != nil {
		return out, err
	}
	profiles, err := profile.Load(profile.DefaultPath())
	if err != nil {
		return out, err
	}
	resolve := a.privateRecommendations
	if resolve == nil {
		resolve = (RecommendedVersionResolver{Cache: &defaultRecommendedCache, Refresh: refresh}).RecommendedVersions
	}
	recommended := make(chan []RecommendedVersion, 1)
	go func() { recommended <- resolve(ctx) }()
	out.Profiles = a.Doctors(ctx, profiles.Profiles)
	out.RecommendedVersions = <-recommended
	return out, nil
}
func (a *Runtime) InstallPrivateRuntime(ctx context.Context, harness, version string) (PrivateRuntimeSnapshot, error) {
	installCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := a.EnsureCatalog(); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if _, err := a.Bundles.InstallAndSelect(installCtx, harness, version); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}

// privateRuntimeLogin serializes cancellation with native login completion.
type privateRuntimeLogin struct {
	control sync.Mutex
	flow    *NativeLogin
	signIn  *privateSignIn
	// harness is set for a sign-in that has no account of its own.
	harness string
	// target is the saved account a sign-in again writes, when the flow has no
	// account of its own.
	target string
}

type PrivateLogin struct {
	AccountID string `json:"accountId"`
	// Harness names the harness of a sign-in that has no account of its own.
	Harness string `json:"harness,omitempty"`
	// Target names the saved account a sign-in again writes, for a flow that has
	// no account of its own, such as Pi's provider sign-in.
	Target  string       `json:"target,omitempty"`
	Warning string       `json:"warning,omitempty"`
	SignIn  *SignInState `json:"signIn,omitempty"`
}

func (a *Runtime) BeginPrivateRuntimeLogin(ctx context.Context, harness, version, label string, _ bool) (PrivateLogin, error) {
	h, err := CanonicalHarness(harness)
	if err != nil {
		return PrivateLogin{}, err
	}
	b, err := EnsureBundle(ctx, h, version)
	if err != nil {
		return PrivateLogin{}, err
	}
	if h == "pi" {
		return a.beginPiProviderSignIn(b.Executable, "")
	}
	n, err := a.Accounts.Create(harness, label)
	if err != nil {
		return PrivateLogin{}, err
	}
	return a.startPrivateLogin(n.ID, b.Executable)
}

// PreparePersonalLogin installs Dyna's copy when it is missing, then looks for the
// user's own login for harness. A nil result means a normal sign-in.
func (a *Runtime) PreparePersonalLogin(ctx context.Context, harness, version string) (*PersonalLogin, error) {
	if _, err := EnsureBundle(ctx, harness, version); err != nil {
		return nil, err
	}
	return a.FindPersonalLogin(ctx, harness)
}

// UsePersonalLoginSnapshot imports the personal login and refreshes the runtime snapshot.
func (a *Runtime) UsePersonalLoginSnapshot(ctx context.Context, harness string) (PrivateRuntimeSnapshot, error) {
	if _, err := a.UsePersonalLogin(ctx, harness); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}
func (a *Runtime) startPrivateLogin(id, exe string) (PrivateLogin, error) {
	n, err := a.Accounts.Find(id)
	if err != nil {
		return PrivateLogin{}, err
	}
	spec, ok := signInSpecs[n.Harness]
	if !ok {
		return a.unavailableSignIn(id, n.Harness), nil
	}
	if openCodeKeySignIn(n) {
		spec = openCodeGoSignInSpec
	}
	flow, err := a.Accounts.BeginLogin(a.lifetimeContext(), id, exe)
	if err != nil {
		return PrivateLogin{}, err
	}
	return a.startPrivateSignIn(id, n.Harness, spec, flow)
}

func (a *Runtime) RestartPrivateRuntimeLogin(ctx context.Context, id string, _ bool) (PrivateLogin, error) {
	harness, target := "pi", ""
	if id != piProviderSignInID {
		n, err := a.Accounts.Find(id)
		if err != nil {
			return PrivateLogin{}, err
		}
		harness = n.Harness
		if harness == "pi" {
			// Pi keeps its provider entries in one login, so the card has no
			// account of its own; target keeps this account's entry its own.
			target = n.ID
		}
	}
	b, err := EnsureBundle(ctx, harness, "")
	if err != nil {
		return PrivateLogin{}, err
	}
	if harness == "pi" {
		return a.beginPiProviderSignIn(b.Executable, target)
	}
	return a.startPrivateLogin(id, b.Executable)
}

// loginReady checks a completed login the way the pane's Finish sign-in does.
// A capture can succeed and still leave no usable account, so status and
// expiry decide whether the sign-in is really done.
func loginReady(result NativeAccount) error {
	if result.Status != "ready" {
		if result.ExpiresAt != nil && !result.ExpiresAt.After(time.Now()) {
			return fmt.Errorf("the new private login expired at %s; sign in again", result.ExpiresAt.UTC().Format(time.RFC3339))
		}
		return errors.New("private sign-in is not complete")
	}
	return nil
}

func (a *Runtime) FinishPrivateRuntimeLogin(ctx context.Context, id string) (PrivateRuntimeSnapshot, error) {
	login := a.findPrivateLogin(id)
	if login == nil {
		return PrivateRuntimeSnapshot{}, errors.New("private sign-in is not running")
	}
	login.control.Lock()
	defer login.control.Unlock()
	if login.flow == nil {
		return PrivateRuntimeSnapshot{}, errors.New(login.signIn.snapshot().Message)
	}
	result, err := login.flow.Complete(a.lifetimeContext())
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if err := loginReady(result); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	a.privateLoginMu.Lock()
	if a.privateLogins[id] == login {
		delete(a.privateLogins, id)
	}
	a.privateLoginMu.Unlock()
	return a.PrivateRuntimes(ctx)
}

// CancelPrivateLogins stops all native sign-ins before releasing their login gates.
func (a *Runtime) CancelPrivateLogins() {
	for _, login := range a.PrivateRuntimeLogins() {
		_ = a.cancelPrivateLogin(login.AccountID)
	}
}
func (a *Runtime) PreviewPrivateConfig(id string) (PreferencePreview, error) {
	return a.PreviewPreferenceImport(id)
}
func (a *Runtime) ImportPrivateConfig(ctx context.Context, id, hash string) (PrivateRuntimeSnapshot, error) {
	if err := a.ImportPreferences(id, hash); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	n, err := a.Accounts.Find(id)
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if err := a.Regenerate(n.Harness); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}

// ActivatePrivateRuntimeAccount makes an account active when no turn of its
// harness runs, then regenerates that harness's config. A running turn is an
// error: the web app's switch waits for turns instead.
func (a *Runtime) ActivatePrivateRuntimeAccount(ctx context.Context, id string) error {
	n, err := a.Accounts.Find(id)
	if err != nil {
		return err
	}
	bundle, err := a.Bundles.Resolve(n.Harness, "")
	if err != nil {
		return err
	}
	if err := a.Accounts.SwitchIfIdle(ctx, n.ID, bundle.Executable); err != nil {
		return err
	}
	return a.Regenerate(n.Harness)
}

// GCPrivateRuntime removes unused bundles, records of retired harnesses and
// unused config generations. It runs in the engine process, so an account
// record it drops publishes the account change.
func (a *Runtime) GCPrivateRuntime(ctx context.Context) error {
	pins, err := OpenPins()
	if err != nil {
		return err
	}
	if err := a.Bundles.GC(pins); err != nil {
		return err
	}
	if err := RemoveRetiredHarnesses(a.Accounts, a.Bundles); err != nil {
		return err
	}
	return a.GCGenerations(ctx)
}

func (a *Runtime) RemovePrivateRuntimeAccount(ctx context.Context, id string, confirmed bool) (PrivateRuntimeSnapshot, error) {
	if AccountInUse(id) {
		return PrivateRuntimeSnapshot{}, errors.New("an open session uses this private account; close it before removal")
	}
	if err := a.Accounts.Remove(id, confirmed); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}

// SetAccountRepos limits an account to repositories. It is an admin-scope engine
// call, so only the owner's paired devices reach it.
// Each repo is a remote URL or host/org/name; an empty list removes the limit.
func (a *Runtime) SetAccountRepos(ctx context.Context, id string, repos []string) (PrivateRuntimeSnapshot, error) {
	if _, err := a.Accounts.Find(id); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	identities := make([]string, 0, len(repos))
	for _, r := range repos {
		identity, err := reposcope.Identity(r)
		if err != nil {
			return PrivateRuntimeSnapshot{}, err
		}
		identities = append(identities, identity)
	}
	if err := a.Accounts.SetRepos(id, identities); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}
func (a *Runtime) SetHarnessConfigEnabled(ctx context.Context, kind, name, harness string, enabled bool) (config.ConfigSnapshot, error) {
	m := a.Config
	if err := m.SetEnabled(ctx, kind, name, harness, enabled); err != nil {
		return config.ConfigSnapshot{}, err
	}
	if err := a.Regenerate(harness); err != nil {
		return config.ConfigSnapshot{}, err
	}
	return m.Catalog("")
}
func (a *Runtime) SyncPrivateRuntime(ctx context.Context, harness string) (PrivateRuntimeSnapshot, error) {
	if err := authority.Check("SyncPrivateRuntime", authority.Of(ctx), nil); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if err := a.Regenerate(harness); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}

// UninstallPrivateRuntime removes every Dyna copy of a harness.
func (a *Runtime) UninstallPrivateRuntime(ctx context.Context, harness string, confirmed bool) (PrivateRuntimeSnapshot, error) {
	pins, err := OpenPins()
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if err = a.Bundles.Uninstall(ctx, harness, pins, confirmed); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}

func (a *Runtime) RemovePrivateRuntime(ctx context.Context, harness, version string, confirmed bool) (PrivateRuntimeSnapshot, error) {
	pins, err := OpenPins()
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if err = a.Bundles.Remove(ctx, harness, version, pins, confirmed); err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return a.PrivateRuntimes(ctx)
}
