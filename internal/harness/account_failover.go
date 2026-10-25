package harness

import (
	"context"
	"fmt"
	"time"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
)

// AccountSwitch names the accounts of a turn that moved after a usage limit.
type AccountSwitch struct {
	From, To string
	Until    time.Time // when From recovers
}

// FailoverAccount switches the harness to another account that may run in the
// turn's repo. It returns nil, nil when the profile has no Dyna-managed
// account. Tests replace it.
var FailoverAccount = failoverAccount

func failoverAccount(ctx context.Context, p profile.Profile, cwd string, accountID string, failure error) (*AccountSwitch, error) {
	if !p.DynaManaged() {
		return nil, nil
	}
	rt := harnessruntime.New()
	b, _, err := (&harnessruntime.FirstUse{Bundles: rt.Bundles}).EnsureBundle(ctx, p.Harness, p.RuntimeVersion)
	if err != nil {
		return nil, err
	}
	until := UnavailableUntil(failure.Error(), time.Now())
	from, to, err := rt.Accounts.Failover(ctx, p.Harness, accountID, harnessruntime.RepoIdentity(ctx, cwd), b.Executable, until)
	if err != nil {
		return nil, err
	}
	return &AccountSwitch{From: from.Label, To: to.Label, Until: until}, nil
}

// accountFailoverPrompt continues an exact session that ran on the account
// that hit its limit.
const accountFailoverPrompt = "Your account hit its usage limit, so Dyna moved this session to another account. Continue the task from where you stopped. Do not repeat work or actions you already did."

// withAccountFailover runs a turn. When it ends on a quota failure, it moves the
// harness to the next allowed account and runs the turn once more there. A
// rejected sign-in never moves the account, and a busy provider never does
// either: it gets a backoff and then another model instead.
// A turn that already did work resumes its exact session; if the harness gave
// no session to resume, the turn is not replayed. When no account is left, the
// error says when the first one resets.
func withAccountFailover(ctx context.Context, p profile.Profile, cwd string, exec ExecutionOptions, once func(opts ExecutionOptions, resumeID string) (Result, error)) (Result, error) {
	res, err := once(exec, "")
	if err == nil || isNativeAdmissionError(err) || ctx.Err() != nil || exec.ProcessRunner != nil || !providerfailure.Is(err, providerfailure.Quota) {
		return res, err
	}
	worked := res.SessionID != "" || res.Output != "" || res.Usage.Total() > 0
	if worked && res.SessionID == "" {
		return res, fmt.Errorf("%w; the turn already did work and has no session to resume, so it was not replayed on another account", err)
	}
	sw, ferr := FailoverAccount(ctx, p, cwd, res.AccountID, err)
	if ferr != nil {
		return res, fmt.Errorf("%w; no account to move to: %v", err, ferr)
	}
	if sw == nil {
		return res, err
	}
	exec.AfterAccountSwitch = true
	res, err = once(exec, res.SessionID)
	res.AccountSwitch = sw
	return res, err
}
