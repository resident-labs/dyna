package engine

import (
	"context"

	"dyna-agent/internal/account/importer"
	accountstore "dyna-agent/internal/account/store"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// accountsImport moves a saved native harness sign-in into the Pi account of
// its provider. dryRun reports the planned move and writes nothing. The engine
// owns the profile and session stores, so it also reports who still runs the
// harness: a move retires the source and must not break a profile or a turn.
func (s services) accountsImport(ctx context.Context, harness, account string, dryRun bool) (importer.Result, error) {
	use, err := s.accountHarnessUse(harness)
	if err != nil {
		return importer.Result{}, err
	}
	im := &importer.Importer{
		Store: &accountstore.Store{Root: s.runtime.Accounts.Root},
		Host:  harnessruntime.PiImportHost{Runtime: s.runtime},
	}
	return im.Run(ctx, importer.Request{Harness: harness, Account: account, DryRun: dryRun, Use: use})
}

// accountHarnessUse reads who runs the harness now: every enabled profile and
// every open session, which the session store resolves to its profile.
func (s services) accountHarnessUse(harness string) (importer.Use, error) {
	manager := s.rooms.Notify.Sessions
	if manager == nil {
		return importer.Use{}, nil
	}
	profiles, err := manager.ProfileStore()
	if err != nil {
		return importer.Use{}, err
	}
	sessions := []subagent.Meta{}
	if manager.Store != nil {
		if sessions, err = manager.Store.ListOpenMeta(); err != nil {
			return importer.Use{}, err
		}
	}
	return harnessUse(harness, profiles.Profiles, sessions), nil
}

// harnessUse names the enabled profiles and the open sessions that run on one
// harness. A session refuses the harness even when its own profile is disabled,
// because the turn it runs still uses the harness.
func harnessUse(harness string, profiles []profile.Profile, sessions []subagent.Meta) importer.Use {
	byName := make(map[string]profile.Profile, len(profiles))
	use := importer.Use{}
	for _, p := range profiles {
		byName[p.Name] = p
		if !p.Disabled && p.Harness == harness {
			use.Profiles = append(use.Profiles, importer.UseProfile{Name: p.Name, Harness: p.Harness})
		}
	}
	for _, m := range sessions {
		p, ok := byName[m.Profile]
		if !ok || p.Harness != harness {
			continue
		}
		ref := m.Name
		if ref == "" {
			ref = m.ID
		}
		use.Sessions = append(use.Sessions, importer.UseSession{Ref: ref, Harness: p.Harness})
	}
	return use
}
