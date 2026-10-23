package roomsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

// ServiceView is one Dyna service as its page header shows it. Pinned is the
// stored profile ("" means Auto); Profile is the one the next turn uses.
// Scope is "global" when one setting covers every room, or "room", as in rules.
// Profiles names the enabled profiles a service can run, for its menu.
type ServiceView struct {
	Role     string   `json:"role"`
	Name     string   `json:"name"`
	Page     string   `json:"page"`
	Profile  string   `json:"profile"`
	Pinned   string   `json:"pinned"`
	Source   string   `json:"source"`
	Reason   string   `json:"reason"`
	Scope    string   `json:"scope"`
	Sessions int      `json:"sessions"`
	Profiles []string `json:"profiles"`
}

// ListServices reads each service's profile from the setting that owns it:
// settings for the steward, and the room's reviewer state for the
// reviewer. A service whose state cannot be read keeps its row, with the
// error as its Reason, so the other services still show.
func (s *Service) ListServices(roomRef string) ([]ServiceView, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return nil, err
	}
	steward := ServiceView{Role: subagent.RoleSteward, Name: "Merge steward", Page: "pull-requests", Pinned: v.MergeStewardProfile, Scope: "global"}
	steward.Profile, steward.Reason, err = s.PRs.Watcher().StewardProfile(v.MergeStewardProfile)
	if err != nil {
		steward.Reason = err.Error()
	}
	reviewer := ServiceView{Role: "reviewer", Name: "PR reviewer", Page: "pull-requests", Scope: "room"}
	p := s.PRs.Watcher()
	if st, err := p.Store.Read(); err != nil {
		reviewer.Reason = err.Error()
	} else if status, err := p.ReviewerStatus(rec.ID); err != nil {
		reviewer.Reason = err.Error()
	} else {
		reviewer.Profile, reviewer.Pinned, reviewer.Reason = status.Profile, st.Reviewers[rec.ID].Profile, status.ProfileReason
		if reviewer.Profile == "" {
			reviewer.Reason = status.LastError
		}
	}
	live, err := s.sessions().ListSnapshot(false)
	if err != nil {
		return nil, err
	}
	store, err := s.Notify.Sessions.ProfileStore()
	if err != nil {
		return nil, err
	}
	choices := []string{}
	for _, p := range store.Profiles {
		if p.ForServices() {
			choices = append(choices, p.Name)
		}
	}
	out := []ServiceView{reviewer, steward}
	for i := range out {
		out[i].Profiles = choices
		out[i].Source = "auto"
		if out[i].Pinned != "" {
			out[i].Source = "pinned"
		}
		for _, info := range live {
			if info.Role == out[i].Role && info.Status != subagent.StatusClosed && (out[i].Scope == "global" || info.RoomID == rec.ID) {
				out[i].Sessions++
			}
		}
	}
	return out, nil
}

// SetServiceProfile pins a service to an enabled profile; "" means Auto.
func (s *Service) SetServiceProfile(ctx context.Context, roomRef, role, profile string) error {
	if err := authority.Check("SetServiceProfile", authority.Of(ctx), nil); err != nil {
		return err
	}
	switch role {
	case subagent.RoleSteward:
		if _, err := s.rooms().Resolve(roomRef); err != nil {
			return err
		}
		if profile != settings.StewardProfileOff {
			if err := s.enabledProfile(profile); err != nil {
				return err
			}
		}
		key := settings.KeyMergeStewardProfile
		_, err := configsvc.Set(ctx, key, profile)
		return err
	case "reviewer":
		return s.PRs.SetReviewerProfile(ctx, roomRef, profile)
	}
	return fmt.Errorf("unknown service %q", role)
}

// enabledProfile accepts "" (Auto) or the name of an enabled profile.
func (s *Service) enabledProfile(name string) error {
	if name == "" {
		return nil
	}
	profiles, err := s.Notify.Sessions.ProfileStore()
	if err != nil {
		return err
	}
	if profile.IsEnabled(profiles.Profiles, name) {
		return nil
	}
	return fmt.Errorf("no enabled profile named %q", name)
}
