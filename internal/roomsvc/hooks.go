package roomsvc

import (
	"context"
	"sort"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
)

// HookRepos lists configured hook repositories and active room repositories.
func (s *Service) HookRepos(ctx context.Context) ([]string, error) {
	configured, err := hooks.New().Repositories(ctx)
	if err != nil {
		return nil, err
	}
	rooms, err := s.Rooms()
	if err != nil {
		return nil, err
	}
	paths := configured
	for _, r := range rooms {
		if r.Repo != "" && !r.Archived {
			paths = append(paths, r.Repo)
		}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range paths {
		root, err := repoid.Git(ctx, p, "rev-parse", "--show-toplevel")
		if err != nil || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	sort.Strings(out)
	return out, nil
}

// scanHookSpool routes spool changes through the same store watch as other
// worker inputs. The engine's startup pass handles the initial state.
func (s *Service) scanHookSpool(previous *string, first bool, emit func(Event)) {
	version := hooks.New().SpoolVersion()
	if version == *previous {
		return
	}
	*previous = version
	if !first {
		emit(Event{Kind: "hooks", WorkerWake: true})
	}
}
