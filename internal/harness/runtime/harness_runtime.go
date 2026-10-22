package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// privateRuntimeLogin serializes terminal shutdown with native login completion.
type privateRuntimeLogin struct {
	control    sync.Mutex
	finished   bool
	flow       *NativeLogin
	terminalID string
}

type PrivateLogin struct {
	AccountID  string `json:"accountId"`
	TerminalID string `json:"terminalId"`
	Warning    string `json:"warning,omitempty"`
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
	flow, err := a.Accounts.BeginLogin(a.lifetimeContext(), id, exe)
	if err != nil {
		return PrivateLogin{}, err
	}
	argv, err := a.privateSignInCommand(id, exe, flow)
	if err != nil {
		_ = flow.Cancel()
		return PrivateLogin{}, err
	}
	term, err := a.terminals.OpenCommand(argv, flow.Env, flow.Cwd, 90, 24)
	if err != nil {
		_ = flow.Cancel()
		return PrivateLogin{}, err
	}
	a.privateLoginMu.Lock()
	defer a.privateLoginMu.Unlock()
	if a.privateLogins == nil {
		a.privateLogins = map[string]*privateRuntimeLogin{}
	}
	login := &privateRuntimeLogin{flow: flow, terminalID: term.ID}
	a.privateLogins[id] = login
	go a.watchPrivateLogin(a.lifetimeContext(), login)
	return PrivateLogin{AccountID: id, TerminalID: term.ID, Warning: flow.Warning}, nil
}

// privateSignInCommand returns the terminal command for one private sign-in.
func (a *Runtime) privateSignInCommand(id, exe string, flow *NativeLogin) ([]string, error) {
	n, err := a.Accounts.Find(id)
	if err != nil {
		return nil, err
	}
	return privateSignInArgv(n.Harness, exe, flow.Cwd, n.Identity, flow.Argv)
}

// privateSignInArgv runs Antigravity's sign-in through its ACP server, so the
// owner sees the Google link and never the agy CLI's color-scheme and Terms
// screens. Every other harness keeps the command its manager chose. scope is
// the native login scope; the helper derives the paired HOME from it.
func privateSignInArgv(harness, exe, scope, identity string, native []string) ([]string, error) {
	if harness != "antigravity" {
		return native, nil
	}
	dyna, err := os.Executable()
	if err != nil {
		return nil, errors.New("Dyna cannot start the Antigravity ACP sign-in.")
	}
	return []string{dyna, "harness", "accounts", acpSignInCommand, "--agy", exe, "--scope", scope, "--expect", identity}, nil
}

func (a *Runtime) RestartPrivateRuntimeLogin(ctx context.Context, id string, _ bool) (PrivateLogin, error) {
	n, err := a.Accounts.Find(id)
	if err != nil {
		return PrivateLogin{}, err
	}
	b, err := EnsureBundle(ctx, n.Harness, "")
	if err != nil {
		return PrivateLogin{}, err
	}
	return a.startPrivateLogin(id, b.Executable)
}
func (a *Runtime) FinishPrivateRuntimeLogin(ctx context.Context, id string) (PrivateRuntimeSnapshot, error) {
	a.privateLoginMu.Lock()
	login := a.privateLogins[id]
	a.privateLoginMu.Unlock()
	if login == nil {
		return PrivateRuntimeSnapshot{}, errors.New("private sign-in is not running")
	}
	login.control.Lock()
	defer login.control.Unlock()
	flow, termID := login.flow, login.terminalID
	exited := false
	for _, term := range a.terminals.List("") {
		if term.ID == termID {
			exited = term.Exited
		}
	}
	if !exited {
		if err := a.terminals.Close(termID); err != nil {
			return PrivateRuntimeSnapshot{}, err
		}
	}
	result, err := flow.Complete(a.lifetimeContext())
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	if result.Status != "ready" {
		if result.ExpiresAt != nil && !result.ExpiresAt.After(time.Now()) {
			return PrivateRuntimeSnapshot{}, fmt.Errorf("the new private login expired at %s; sign in again", result.ExpiresAt.UTC().Format(time.RFC3339))
		}
		return PrivateRuntimeSnapshot{}, errors.New("private sign-in is not complete")
	}
	login.finished = true
	a.privateLoginMu.Lock()
	if a.privateLogins[id] == login {
		delete(a.privateLogins, id)
	}
	a.privateLoginMu.Unlock()
	return a.PrivateRuntimes(ctx)
}

// CancelPrivateLogins stops all native terminals before releasing their login gates.
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

func (a *Runtime) watchPrivateLogin(ctx context.Context, login *privateRuntimeLogin) {
	var offset uint64
	for {
		event, wake, err := a.terminals.Read(login.terminalID, offset)
		if err != nil || event.Exited {
			// An explicit finish or cancel must stop the terminal before the watcher
			// can release the gate. Complete and Cancel are idempotent.
			login.control.Lock()
			defer login.control.Unlock()
			if login.finished {
				return
			}
			login.finished = true
			if err != nil {
				_ = login.flow.Cancel()
				return
			}
			if event.ExitCode == 0 {
				_, _ = login.flow.Complete(ctx)
			} else {
				_ = login.flow.Cancel()
			}
			return
		}
		offset = event.Offset
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
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
