package configsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profilecheck"
)

// ProfileCheck returns fresh support and health after an optional live probe.
// ProbeError describes an unsuccessful probe without losing the observed evidence.
type ProfileCheck struct {
	Name       string         `json:"name"`
	Support    HarnessSupport `json:"support"`
	ProbeError string         `json:"probeError,omitempty"`
}

// CheckProfile is shared by the desktop, web client, and future profile test
// controls. Only an explicit live request starts a model. Subagent-only profiles
// are eligible. The probe enforces its own two-minute deadline.
func CheckProfile(ctx context.Context, name string, live bool) (ProfileCheck, error) {
	if live {
		if err := authority.Check("ConfigCheckProfile", authority.Of(ctx), nil); err != nil {
			return ProfileCheck{}, err
		}
	}
	store, err := loadProfiles()
	if err != nil {
		return ProfileCheck{}, err
	}
	p, ok := store.Get(name)
	if !ok {
		return ProfileCheck{}, fmt.Errorf("no profile named %q", name)
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
