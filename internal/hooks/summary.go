package hooks

import (
	"context"
	"time"

	"dyna-agent/internal/repoid"
)

// Summary is one hook with the newest run recorded for it, so a page can show
// each hook's state without reading every run. LastRun is nil when the hook
// has never run.
type Summary struct {
	Hook    Hook `json:"hook"`
	LastRun *Run `json:"lastRun,omitempty"`
	// NextRun is when the schedule next runs this hook. It is nil for a hook
	// that does not run on a schedule.
	NextRun *time.Time `json:"nextRun,omitempty"`
}

// Summaries lists a repository's hooks in their saved order, each with its
// newest run and its next scheduled run. It reads the runs once, not once per
// hook.
func (s Service) Summaries(ctx context.Context, repo string) ([]Summary, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return s.summaries(ctx, r.ID)
}

// SummariesGlobal lists the global hooks: the built-ins the engine seeds and
// the hooks the owner added for the whole machine.
func (s Service) SummariesGlobal(ctx context.Context) ([]Summary, error) {
	return s.summaries(ctx, globalKey)
}

func (s Service) summaries(ctx context.Context, id string) ([]Summary, error) {
	hooks, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	runs, err := s.runs(ctx, id, "")
	if err != nil {
		return nil, err
	}
	st, err := s.runtimeState(ctx, id)
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
		summary := Summary{Hook: h, LastRun: newest["hook-"+h.Name]}
		if h.Event == Schedule {
			if at := st.Due[scheduleKey(h)]; !at.IsZero() {
				summary.NextRun = &at
			}
		}
		out = append(out, summary)
	}
	return out, nil
}
