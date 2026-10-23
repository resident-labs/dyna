package watch

import (
	"fmt"
	"strings"

	"dyna-agent/internal/room"
)

// staleNotice checks the current record, rather than the snapshot used to queue
// the notice. SupersededBy keeps the reason visible in PR history.
func staleNotice(st State, r Record, n Notice) string {
	if n.Kind == "held-comment" {
		if _, ok := r.HeldComments[n.CommentID]; !ok {
			return "comment-resolved"
		}
		return ""
	}
	// Lifecycle notices and comment batches remain relevant after a push or
	// merge. Only CI results and readiness describe the current head.
	if n.Kind != "ci" && n.Kind != "land" && !legacyCINotice(n.Text) {
		return ""
	}
	_, rest, hasHead := strings.Cut(n.Text, "\nHead: ")
	head, body, hasBody := strings.Cut(rest, "\n\n")
	if r.State == "closed" || r.State == "merged" {
		return "change-ended"
	}
	if hasHead && hasBody && head != r.Head {
		return "head-changed"
	}
	if n.Kind == "land" && !landable(st, r) {
		return "no-longer-landable"
	}
	if n.Kind == "ci" && !EffectivePolicy(st, r).EnforceCITimeBudget() &&
		strings.HasPrefix(body, "CI for "+r.URL+" ") &&
		(strings.Contains(body, ": over CI time budget (") || strings.Contains(body, "failed: tests over ")) {
		return "budget-policy-changed"
	}
	// The durable notice ID includes the head, run attempt and budget verdict.
	// Rebuild it from the current verdicts to check the exact reported fact,
	// without adding fields to the shared store format.
	if n.Kind == "ci" && strings.HasPrefix(body, "CI for "+r.URL+" ") && strings.Contains(body, ": over CI time budget (") {
		// Transfers retain the original notice in history and link it to its
		// replacement. Use that original ID when checking a transferred notice.
		original := n
		for i := len(r.Notices) - 1; i >= 0; i-- {
			if r.Notices[i].SupersededBy == original.ID {
				original = r.Notices[i]
			}
		}
		if r.CIBudgetHead == r.Head {
			for token, reason := range r.CIBudgetRuns {
				if !strings.HasPrefix(reason, "over CI time budget") {
					continue
				}
				id := "msg_" + digest(r.Key + "/ci-budget:" + r.Head + ":" + token + ":" + reason + "/" + original.Target)[:16]
				if original.ID == id {
					return ""
				}
			}
		}
		return "budget-verdict-changed"
	}
	if n.Kind == "ci" && strings.HasPrefix(body, "CI failed (") && r.CI != "failed" {
		return "ci-verdict-changed"
	}
	return ""
}

// reviewNotice identifies assignments and requests to finish an assigned review.
func reviewNotice(n Notice) bool {
	return strings.Contains(n.Text, "Review PR ") || strings.Contains(n.Text, "Assignment input: ") || strings.Contains(n.Text, "You ended your turn without recording a verdict")
}

// systemReviewNotice delivers discussion without user-level escalation.
func systemReviewNotice(r *Record, target, text, token string) {
	notice(r, target, text, token)
	for i := range r.Notices {
		if r.Notices[i].ID == "msg_"+digest(r.Key + "/" + token + "/" + target)[:16] {
			r.Notices[i].System = true
		}
	}
}

// outcomeNotice queues one waking completion notice per PR outcome.
// Cleanup remains a separate informational notice. All merge observation
// paths call endRecord, including publication by the merge engine.
func outcomeNotice(r *Record) {
	id := "msg_" + digest(r.Key + "/outcome/" + r.State)[:16]
	for _, n := range r.Notices {
		if n.ID == id {
			return
		}
	}
	typ := map[string]room.PRType{"merged": room.PRMerged, "closed": room.PRClosed}[r.State]
	r.Notices = append(r.Notices, Notice{
		ID: id, Target: r.Author, Kind: r.State, Type: typ,
		Text: fmt.Sprintf("%s %s. Mark its goal done if it has no other open PRs. Clean up its worktree and end your turn. %s", changeLabel(*r), r.State, changeLocation(*r)),
	})
}
