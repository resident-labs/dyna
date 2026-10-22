package runtime

import (
	"context"
	"errors"
	"time"
)

// PersonalLogin is the owner's own native login for one harness. Saved means
// that a Dyna account already holds the same identity.
type PersonalLogin struct {
	Harness  string `json:"harness"`
	Label    string `json:"label"`
	Saved    bool   `json:"saved"`
	identity string
}

// FindPersonalLogin reads the owner's native login for harness without
// changing it. It returns nil when there is nothing that Sign in can offer: the
// owner is not signed in, or another Dyna account already exists. Dyna copies a
// personal login only into a harness with no saved account.
func (r *Runtime) FindPersonalLogin(ctx context.Context, harness string) (*PersonalLogin, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return nil, err
	}
	// Antigravity signs in through its ACP server. The owner's agy CLI token is
	// a different grant, so there is nothing Sign in could copy.
	if h == "antigravity" {
		return nil, nil
	}
	a := r.Accounts
	if a == nil {
		return nil, errors.New("Private account storage is unavailable.")
	}
	owner, err := ownerLoginHost(a)
	if err != nil {
		return nil, nil
	}
	scope, err := a.HarnessHome(h)
	if err != nil {
		return nil, err
	}
	owner.Executable = r.harnessExecutable(h, "")
	_, env, err := a.scopedManager(h, scope, owner.Executable)
	if err != nil {
		return nil, err
	}
	source, ok := a.ownerLoginManager(h, owner, env)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	captured, err := source.Capture(ctx, "")
	if err != nil || len(captured) == 0 {
		return nil, nil
	}
	c := captured[0]
	found := &PersonalLogin{Harness: h, Label: c.Label, identity: c.Identity}
	if found.Label == "" {
		found.Label = c.Identity
	}
	accounts, err := a.List()
	if err != nil {
		return nil, err
	}
	others := false
	for _, n := range accounts {
		if n.Harness != h || n.Identity == "" {
			continue
		}
		found.Saved = found.Saved || n.Identity == c.Identity
		others = others || n.Identity != c.Identity
	}
	if others && !found.Saved {
		return nil, nil
	}
	return found, nil
}

// UsePersonalLogin copies the owner's native login into Dyna once, because the
// user chose it, and returns the account that now holds it.
func (r *Runtime) UsePersonalLogin(ctx context.Context, harness string) (NativeAccount, error) {
	found, err := r.FindPersonalLogin(ctx, harness)
	if err != nil {
		return NativeAccount{}, err
	}
	if found == nil {
		return NativeAccount{}, errNoPersonalLogin
	}
	if found.Saved {
		return NativeAccount{}, errors.New("This personal login is already saved in Dyna.")
	}
	if err = r.copyPersonalLogin(ctx, found.Harness); err != nil {
		return NativeAccount{}, err
	}
	n, err := r.Accounts.Active(found.Harness)
	if err != nil || n.Identity != found.identity {
		return NativeAccount{}, errCopyFailed
	}
	return n, nil
}
