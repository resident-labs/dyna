package watch

import (
	"context"
	"fmt"
	"log"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// mergePR names one PR of a merge trial for the card.
func mergePR(r Record) room.MergePR {
	return room.MergePR{Number: r.Number, Title: r.Title, URL: r.URL}
}

// mergeRepo is the repository the card names.
func mergeRepo(r Record) string {
	if r.Repo != "" {
		return r.Repo
	}
	return defaultRepo(r)
}

// mergeTrialPRs names every member of a trial for the card. A member whose
// record is gone adds nothing; the trial events of that member no longer exist
// either.
func mergeTrialPRs(st State, b state.MergeTrial) []room.MergePR {
	var prs []room.MergePR
	for _, change := range b.Changes {
		member := st.Records[change.Key]
		if member == nil {
			continue
		}
		prs = append(prs, mergePR(*member))
	}
	return prs
}

// mergeTrialEvent builds the facts every event of one trial carries. The caller
// sets the step and the facts that only it knows.
func mergeTrialEvent(st State, b state.MergeTrial, step room.MergeStep) room.MergeEvent {
	ev := room.MergeEvent{Type: step, TrialID: trialIdentity(b), Base: b.DefaultBranch, PRs: mergeTrialPRs(st, b)}
	for _, change := range b.Changes {
		if member := st.Records[change.Key]; member != nil {
			ev.Repo = mergeRepo(*member)
			break
		}
	}
	return ev
}

// mergeChecksEvent is the trial's CI progress. The counts are the checks the
// steward already read, so a progress step costs no GitHub call.
func mergeChecksEvent(st State, b state.MergeTrial, ci trialCI) room.MergeEvent {
	ev := mergeTrialEvent(st, b, room.MergeChecks)
	ev.Summary = fmt.Sprintf("%d of %d checks done", ci.Done, ci.Total)
	ev.Checks = &room.MergeCheckSummary{Done: ci.Done, Total: ci.Total, Failed: ci.Failed}
	// Each count is one progress step: a later step is a new post of the same
	// trial, and a repeated one keeps its post.
	ev.Key = fmt.Sprintf("%d/%d", ci.Done, ci.Total)
	return ev
}

// trialAnchors returns one member record per room of the trial, in a stable
// order. A room gets one card, so a trial of PRs from several rooms posts to
// each of them once.
func trialAnchors(st State, b state.MergeTrial) []Record {
	var out []Record
	seen := map[string]bool{}
	for _, change := range b.Changes {
		member := st.Records[change.Key]
		if member == nil || seen[member.RoomID] {
			continue
		}
		seen[member.RoomID] = true
		out = append(out, *member)
	}
	return out
}

// noteTrialEvent posts one quiet merge event to each room of the trial. A
// failed post is logged, like a failed history write: the merge path never
// stops for a card.
func (s *Service) noteTrialEvent(ctx context.Context, st State, b state.MergeTrial, ev room.MergeEvent) {
	token := "merge-" + string(ev.Type) + "/" + ev.Key + "/" + trialIdentity(b)
	for _, anchor := range trialAnchors(st, b) {
		if err := s.postMergeNotice(ctx, anchor, token, Notice{Merge: &ev}); err != nil {
			log.Printf("merge %s event for %s: %v", ev.Type, trialIdentity(b), err)
		}
	}
}
