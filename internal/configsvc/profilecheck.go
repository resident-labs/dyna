package configsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/profilecheck"
)

// CheckProfile is shared by the desktop, web client, and future profile test
// controls. Only an explicit live request starts a model. Subagent-only profiles
// are eligible. The probe enforces its own two-minute deadline.
func CheckProfile(ctx context.Context, name string, live bool) (ProfileCheck, error) {
	if live {
		if err := authority.Check("ConfigCheckProfile", authority.Of(ctx), nil); err != nil {
			return ProfileCheck{}, err
		}
	}
	reg, err := loadProfilesForRepair()
	if err != nil {
		return ProfileCheck{}, err
	}
	p, ok := reg.Store.Get(name)
	if !ok {
		return ProfileCheck{}, fmt.Errorf("no profile named %q", name)
	}
	// The checked profile itself must be launchable, whatever other saved
	// profiles are.
	if report := reg.Reports[name]; report.Status != profile.StatusOK {
		return ProfileCheck{}, fmt.Errorf("profile %q cannot run: %s", name, report.Detail)
	}
	if p.Disabled {
		return ProfileCheck{}, fmt.Errorf("profile %q is disabled", name)
	}
	result := ProfileCheck{Name: name}
	if live {
		if err := profilecheck.Run(ctx, *p); err != nil {
			result.ProbeError = err.Error()
		}
	}
	result.Support = supportFor(*p)
	return result, nil
}
