package roomsvc

import (
	"context"
	"fmt"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
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

// ListServices reads each service's profile from the state that owns it: the
// room's reviewer state for the reviewer. A service whose state cannot be read
// keeps its row, with the error as its Reason, so the other services still show.
func (s *Service) ListServices(roomRef string) ([]ServiceView, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	reviewer := ServiceView{Role: "reviewer", Name: "PR reviewer", Page: "pull-requests", Scope: "room"}
	p := s.PRs.Watcher()
	if st, err := p.Store.Read(); err != nil {
		reviewer.Reason = err.Error()
	} else if status, err := p.ReviewerStatus(rec.ID); err != nil {
		reviewer.Reason = err.Error()
	} else {
		reviewer.Profile, reviewer.Pinned, reviewer.Reason = status.Profile, st.Reviewers[rec.ID].Profile, status.ProfileReason
		if status.ProfileCause != "" && reviewer.Pinned != "" {
			reviewer.Profile = reviewer.Pinned
			reviewer.Reason = "waiting: " + status.ProfileReason
		} else if reviewer.Profile == "" {
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
	out := []ServiceView{reviewer, s.librarianView(rec)}
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

// SetServiceProfile pins a service to an enabled profile; "" means auto.
// Library and reviewer use the same rule: the owner may change any room,
// and a root may change only its own room.
func (s *Service) SetServiceProfile(ctx context.Context, roomRef, role, profile string) error {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return err
	}
	return s.writeServiceProfile(ctx, rec, role, profile)
}

// SetServiceProfileAll pins one service in every room, including archived
// rooms. Only the owner may do it. A library pin also updates the global
// default, so a later room with no pin uses the same profile.
func (s *Service) SetServiceProfileAll(ctx context.Context, role, profile string) error {
	if err := authority.Check("SetServiceProfileAll", authority.Of(ctx), nil); err != nil {
		return err
	}
	if err := s.usableServiceProfile(profile); err != nil {
		return err
	}
	if err := knownService(role); err != nil {
		return err
	}
	rooms, err := s.rooms().List(true)
	if err != nil {
		return err
	}
	if role == "library" {
		if err := s.setGlobalLibraryProfile(profile); err != nil {
			return err
		}
	}
	for _, rec := range rooms {
		if err := s.writeServiceProfile(ctx, rec, role, profile); err != nil {
			return err
		}
	}
	return nil
}

func knownService(role string) error {
	switch role {
	case "reviewer", "library":
		return nil
	default:
		return fmt.Errorf("unknown service %q", role)
	}
}

func (s *Service) usableServiceProfile(name string) error {
	if s.PRs == nil {
		return fmt.Errorf("service profile store is unavailable")
	}
	return s.PRs.Watcher().UsableServiceProfile(name)
}

func (s *Service) writeServiceProfile(ctx context.Context, rec room.Record, role, profile string) error {
	if err := authority.MaySetServiceProfile(authority.Of(ctx), rec.ID, rec.Name); err != nil {
		return err
	}
	if err := s.usableServiceProfile(profile); err != nil {
		return err
	}
	switch role {
	case "reviewer":
		return s.PRs.SetReviewerProfile(ctx, rec.ID, profile)
	case "library":
		return s.rooms().SetLibraryPin(rec.ID, true, profile)
	default:
		return fmt.Errorf("unknown service %q", role)
	}
}

func (s *Service) setGlobalLibraryProfile(profile string) error {
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return err
	}
	if err := v.Set(settings.KeyLibraryProfile, profile); err != nil {
		return err
	}
	return v.Save()
}

// librarianView is the librarian row. A pin that cannot run stays the shown
// profile, with waiting and the reason. Auto keeps an empty pin.
func (s *Service) librarianView(rec room.Record) ServiceView {
	view := ServiceView{Role: "librarian", Name: "Librarian", Page: "library", Scope: "global"}
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		view.Reason = err.Error()
		if rec.LibraryPinSet {
			view.Pinned, view.Profile, view.Scope = rec.LibraryPin, rec.LibraryPin, "room"
		}
		return view
	}
	pin := v.LibraryProfile
	if rec.LibraryPinSet {
		pin = rec.LibraryPin
		view.Scope = "room"
	}
	view.Pinned = pin
	view.Profile = pin
	if pin == "" {
		return view
	}
	if rec.Repo == "" {
		return view
	}
	repo, err := repoid.Resolve(context.Background(), rec.Repo)
	if err != nil {
		return view
	}
	if !v.LibraryOn(repo.Main) && !v.LibraryOn(rec.Repo) {
		return view
	}
	st, err := (librarianstate.Store{DataDir: store.DataDir()}).Get(repo.ID)
	if err != nil {
		return view
	}
	if s.Notify == nil || s.Notify.Sessions == nil {
		return view
	}
	ps, err := s.Notify.Sessions.ProfileStore()
	if err != nil {
		return view
	}
	recent, err := rolesession.RecentProfileFailures(s.Notify.Sessions.Store, time.Now())
	if err != nil {
		recent = nil
	}
	failed := rolesession.MergeFailures(st.FailedProfiles, recent)
	choice, err := rolesession.ChooseProfile(ps.Profiles, pin, runtime.RepoIdentity(context.Background(), rec.Repo), failed, time.Now(), "librarian")
	if err != nil || choice.Cause == "" {
		return view
	}
	view.Profile = pin
	view.Reason = "waiting: " + choice.Reason
	return view
}
