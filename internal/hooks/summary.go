package hooks

import (
	"context"

	"dyna-agent/internal/repoid"
)

// Summary is one hook with the newest run recorded for it, so a page can show
// each hook's state without reading every run. LastRun is nil when the hook
// has never run.
type Summary struct {
	Hook    Hook `json:"hook"`
	LastRun *Run `json:"lastRun,omitempty"`
}

// Summaries lists a repository's hooks in their saved order, each with its
// newest run. It reads the runs once, not once per hook.
func (s Service) Summaries(ctx context.Context, repo string) ([]Summary, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	hooks, err := s.load(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	runs, err := s.runs(ctx, r.ID, "")
	if err != nil {
		return nil, err
	}
	newest := make(map[string]*Run, len(hooks))
	for i := range runs {
		run := &runs[i]
		if last, ok := newest[run.Name]; !ok || run.StartedAt.After(last.StartedAt) {
			newest[run.Name] = run
		}
	}
	out := make([]Summary, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, Summary{Hook: h, LastRun: newest["hook-"+h.Name]})
	}
	return out, nil
}
