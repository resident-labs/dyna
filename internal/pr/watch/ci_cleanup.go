package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"
)

// cleanupCIRuns runs after the PR state and cleanup marker are saved.
// keepHead preserves runs for the current head; an empty value cancels all active runs.
// Errors do not change the PR state or prevent later watcher work.
func (s *Service) cleanupCIRuns(ctx context.Context, r Record, keepHead string) {
	if changeKind(r) != "github" || r.Number < 1 || r.Branch == "" || r.Branch == "main" || r.Branch == r.BaseRef {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, err := s.Store.Read()
	if err != nil {
		log.Printf("PR CI cleanup %s: %s", r.Key, Sanitize(err.Error()))
		return
	}
	gh := s.github(r.RoomID)
	gh.requests = &requestCache{entries: map[string]cachedResponse{}, used: map[string]cachedResponse{}, rates: st.RateLimits, legacyPause: st.LegacyPause, now: s.now}
	err = gh.cancelPRRuns(ctx, refRecord(r), r.Branch, keepHead)
	saveErr := s.Store.update(func(live *State) error {
		applyRateLimits(live, st.RateLimits, s.now())
		return nil
	})
	if err = errors.Join(err, saveErr); err != nil {
		log.Printf("PR CI cleanup %s: %s", r.Key, Sanitize(err.Error()))
	}
}

func (g GitHub) cancelPRRuns(ctx context.Context, ref Ref, branch, keepHead string) error {
	// Check GitHub itself so untracked PRs also protect a shared branch.
	pulls, err := g.openPulls(ctx, ref)
	if err != nil {
		return err
	}
	for _, pull := range pulls {
		if pull.Number != ref.Number && pull.Head.Ref == branch {
			return nil
		}
	}
	var pages []struct {
		Runs []Run `json:"workflow_runs"`
	}
	path := fmt.Sprintf("repos/%s/actions/runs?branch=%s&event=pull_request&per_page=100", ref.Repo, url.QueryEscape(branch))
	if err := g.pages(ctx, ref, path, &pages); err != nil {
		return err
	}
	var failures []error
	for _, page := range pages {
		for _, run := range page.Runs {
			if run.HeadBranch != branch || run.Event != "pull_request" || (keepHead != "" && (run.HeadSHA == "" || run.HeadSHA == keepHead)) {
				continue
			}
			switch run.Status {
			case "queued", "waiting", "requested", "pending", "in_progress":
				if err := g.CancelRun(ctx, ref, run); err != nil {
					failures = append(failures, fmt.Errorf("cancel run %d: %w", run.ID, err))
				}
			}
		}
	}
	return errors.Join(failures...)
}
