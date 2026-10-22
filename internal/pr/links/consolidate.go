package links

import (
	"context"
	"encoding/json"
	"slices"

	prstate "dyna-agent/internal/pr/state"
)

// Consolidate updates live ownership through the shared PR persistence path.
func Consolidate(root, roomID, target string, absorbed map[string]bool) error {
	revision, err := prstate.Revision(root)
	if err != nil {
		return err
	}
	if revision == "missing" {
		return nil
	}
	return prstate.Update(context.Background(), root, func(st *prstate.State) error {
		for _, r := range st.Records {
			if r == nil || r.RoomID != roomID || r.State != "open" {
				continue
			}
			// Stopping a watch does not end the PR or its author responsibility.
			if absorbed[r.Author] {
				if !slices.Contains(r.FormerAuthors, r.Author) {
					r.FormerAuthors = append(r.FormerAuthors, r.Author)
				}
				r.Author = target
			}
			if absorbed[r.Parent] {
				r.Parent = target
			}
			if r.Watching && absorbed[r.ReviewerOverride] {
				r.ReviewerOverride = ""
			}
			if r.Watching && absorbed[r.Reviewer] && r.Verdict == "" {
				raw, err := json.Marshal(r)
				if err != nil {
					return err
				}
				fields := map[string]json.RawMessage{}
				if err = json.Unmarshal(raw, &fields); err != nil {
					return err
				}
				saved := map[string]json.RawMessage{}
				for _, key := range []string{"reviewerSession", "reviewHead", "reviewRound", "reviewPublishing", "reviewId"} {
					if value, ok := fields[key]; ok {
						saved[key] = value
					}
				}
				r.ReviewHistory = append(r.ReviewHistory, saved)
				r.Reviewer, r.RoundHead = "", ""
				r.PauseReason = "review paused: consolidation requires a new independent reviewer assignment"
			}
			for i := range r.Notices {
				if absorbed[r.Notices[i].Target] {
					r.Notices[i].Target = target
				}
			}
		}
		return nil
	})
}
