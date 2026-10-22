package facts

import "dyna-agent/internal/pr/state"

// MissingRequiredReviewers derives requirements from the existing per-reviewer
// verdict history. Only the latest verdict for this reviewer and head counts.
func MissingRequiredReviewers(r state.Record) []string {
	var missing []string
	for _, reviewer := range r.RequiredReviewers {
		approved := false
		for i := len(r.ReviewRounds) - 1; i >= 0; i-- {
			round := r.ReviewRounds[i]
			if round.Reviewer == reviewer && round.Head == r.Head {
				approved = r.Head != "" && ApprovedVerdict(round.Verdict)
				break
			}
		}
		if !approved {
			missing = append(missing, reviewer)
		}
	}
	return missing
}
