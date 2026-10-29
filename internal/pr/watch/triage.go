package watch

import (
	"regexp"
	"strings"

	"dyna-agent/internal/pr/state"
)

// Triage sees comment text and routing identity, never a diff or code review
// task. The implementation uses rules over a whole PR comment batch.
type PRContext struct {
	URL           string   `json:"url"`
	OwnerLogin    string   `json:"ownerLogin"`
	AuthorSession string   `json:"authorSession"`
	FormerAuthors []string `json:"formerAuthors,omitempty"`
}
type Event struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Commenter   string `json:"commenter"`
	Association string `json:"authorAssociation,omitempty"`
	IsBot       bool   `json:"isBot"`
	Text        string `json:"text"`
	ReviewState string `json:"reviewState,omitempty"`
	// Session is the Dyna session that posted the comment, resolved from the
	// stored comment IDs. It is empty for comments the owner posted by hand.
	Session string `json:"session,omitempty"`
}
type Decision = state.Decision
type Triager interface {
	Triage(PRContext, []Event) []Decision
}
type Rules struct {
	Learned     []NoiseRule
	TrustedBots []string
	// DeliverOutside keeps comments from outside commenters. When it is false
	// they are held until the owner admits them.
	DeliverOutside bool
}

// Rules keeps outside input and holds it only when DeliverOutside is false.
func (r Rules) Triage(pr PRContext, events []Event) []Decision {
	out := make([]Decision, len(events))
	seen := map[string]bool{}
	for i, e := range events {
		d := rule(pr, e)
		if d.Reason != "owner" && d.Reason != "author_marker" && !r.DeliverOutside && !trustedEvent(e, r.TrustedBots) {
			d = Decision{Action: "hold", Reason: "outside_commenter"}
		}
		if d.Action == "keep" && d.Reason != "owner" {
			if learned, ok := learnedDecision(pr, e, r.Learned); ok {
				d = learned
			}
		}
		// Repeated bot issue/review status comments add no second copy. Inline
		// comments can repeat text at different code locations, so keep those.
		// A review that approves or requests changes is a new fact each time.
		key := e.Commenter + "/" + e.Kind + "/" + strings.TrimSpace(e.Text)
		if d.Action == "keep" && d.Reason != "owner" && e.IsBot && e.Kind != "comments" && !reviewStateChange(e) {
			if seen[key] {
				d = Decision{Action: "drop", Reason: "duplicate_bot_comment"}
			} else {
				seen[key] = true
			}
		}
		out[i] = d
	}
	return out
}
func rule(pr PRContext, e Event) Decision {
	who := e.Session
	if who == "" && strings.EqualFold(e.Commenter, pr.OwnerLogin) && pr.OwnerLogin != "" {
		return Decision{Action: "keep", Reason: "owner"}
	}
	formerAuthor := false
	for _, id := range pr.FormerAuthors {
		if who == id {
			formerAuthor = true
		}
	}
	if who != "" && (who == pr.AuthorSession || formerAuthor) {
		return Decision{Action: "drop", Reason: "author_marker"}
	}
	if e.IsBot && e.Kind == "reviews" && e.ReviewState == "COMMENTED" && strings.TrimSpace(e.Text) == "" {
		return Decision{Action: "drop", Reason: "empty_bot_review"}
	}
	if d, ok := builtinNoiseDecision(e); ok {
		return d
	}
	if e.IsBot && !protectedFeedback(e) {
		switch strings.TrimSpace(e.Text) {
		case "Rate limit exceeded", "rate limited":
			return Decision{Action: "drop", Reason: "known_rate_limit_noise"}
		case "Review in progress", "Review in progress...", "Review in progress…":
			return Decision{Action: "drop", Reason: "known_review_placeholder"}
		}
	}
	// TODO: legacy/shim remove later — retains the exact Copilot quota template until shared template rules cover it.
	if strings.EqualFold(e.Commenter, "copilot-pull-request-reviewer[bot]") && strings.TrimSpace(e.Text) == "Copilot was unable to review this pull request because the user who requested the review has reached their quota limit." {
		if protectedFeedback(e) {
			return Decision{Action: "keep", Reason: "possibly_useful_feedback"}
		}
		return Decision{Action: "drop", Reason: "known_rate_limit_noise"}
	}
	// TODO: legacy/shim remove later — preserves exact CodeRabbit wrappers until shared template rules cover them safely.
	knownBot := strings.EqualFold(e.Commenter, "coderabbitai[bot]") || strings.EqualFold(e.Commenter, "coderabbitai")
	if knownBot {
		// These two exact markers identify full CodeRabbit noise messages.
		// Its reply marker also wraps real answers, so do not classify it alone.
		noiseMarker := false
		for _, marker := range []string{"<!-- This is an auto-generated comment: rate limited by coderabbit.ai -->", "<!-- This is an auto-generated comment: skip review by coderabbit.ai -->"} {
			noiseMarker = noiseMarker || strings.Contains(e.Text, marker)
		}
		// CodeRabbit posts findings as a review ("Actionable comments posted")
		// and inline comments, which stay protected. Its PR summary comment
		// wraps generated boilerplate, such as file lists naming
		// "findings.go", that trips the shared guard. Strip only that
		// recognized boilerplate; anything else must still pass the guard.
		if e.Kind == "issue-comments" && strings.Contains(e.Text, codeRabbitSummaryMarker) && !protectedCodeRabbitFeedback(Event{Kind: e.Kind, Text: stripCodeRabbitSummary(e.Text)}) {
			if noiseMarker {
				return Decision{Action: "drop", Reason: "known_bot_noise_marker"}
			}
			return Decision{Action: "drop", Reason: "known_bot_summary"}
		}
		if protectedCodeRabbitFeedback(e) {
			return Decision{Action: "keep", Reason: "possibly_useful_feedback"}
		}
		if noiseMarker {
			return Decision{Action: "drop", Reason: "known_bot_noise_marker"}
		}
		normalized := strings.Join(strings.Fields(invocationMarker.ReplaceAllString(e.Text, "")), " ")
		if normalized == strings.Join(strings.Fields(rateLimitedReply), " ") {
			return Decision{Action: "drop", Reason: "known_rate_limit_reply"}
		}
		switch strings.TrimSpace(e.Text) {
		case "<!-- This is an auto-generated comment: rate limit exceeded by coderabbit.ai -->":
			return Decision{Action: "drop", Reason: "known_rate_limit_noise"}
		}
	}
	return Decision{Action: "keep", Reason: "possibly_useful_feedback"}
}

// TODO: legacy/shim remove later — retain exact CodeRabbit Autofix controls until shared template rules cover them.
// The optional Autofix control is not a review finding. Ignore only its
// exact boilerplate while checking a CodeRabbit status wrapper for
// useful content. Changed controls and any appended findings remain protected.
var codeRabbitAutopilot = regexp.MustCompile(`(?s)<!-- autopilot:start -->.*?<!-- autopilot:end -->`)
var codeRabbitCheckboxID = regexp.MustCompile(`<!-- \{"checkboxId":"[a-fA-F0-9-]+"\} -->`)

const codeRabbitAutofixControl = `<!-- autopilot:start -->
- [ ] <!-- checkbox --> <strong title="Keep fixing CodeRabbit findings and required CI, and resolving merge conflicts">Autofix</strong> · Keep fixing CodeRabbit findings and required CI, and resolving merge conflicts
<!-- autopilot:end -->`

func protectedCodeRabbitFeedback(e Event) bool {
	e.Text = codeRabbitAutopilot.ReplaceAllStringFunc(e.Text, func(block string) string {
		if strings.Join(strings.Fields(codeRabbitCheckboxID.ReplaceAllString(block, "<!-- checkbox -->")), " ") == strings.Join(strings.Fields(codeRabbitAutofixControl), " ") {
			return ""
		}
		return block
	})
	return protectedFeedback(e)
}

const codeRabbitSummaryMarker = "<!-- This is an auto-generated comment: summarize by coderabbit.ai -->"

// codeRabbitSummaryBoilerplate matches generated sections of a CodeRabbit
// summary that never hold findings: its review-view link, run details and
// file lists, plan tips, and the review-limit notice.
var codeRabbitSummaryBoilerplate = regexp.MustCompile(`(?s)<!-- (?:review_stack_entry|recent_review|tips)_start -->.*?<!-- (?:review_stack_entry|recent_review|tips)_end -->` +
	`|<!-- This is an auto-generated comment: rate limited by coderabbit\.ai -->.*?<!-- end of auto-generated comment: rate limited by coderabbit\.ai -->`)

func stripCodeRabbitSummary(text string) string {
	return codeRabbitSummaryBoilerplate.ReplaceAllString(text, "")
}

// TODO: legacy/shim remove later — retain the exact CodeRabbit invocation wrapper until shared template rules cover it.
var invocationMarker = regexp.MustCompile(`<!-- CodeRabbit review command invocation: [a-fA-F0-9-]+ -->`)

const rateLimitedReply = `<!-- This is an auto-generated reply by CodeRabbit -->
<details>
<summary>⚠️ Action not completed</summary>

Review rate limited.

> Note: CodeRabbit is an incremental review system and does not re-review already reviewed commits. This command is applicable only when automatic reviews are paused.

</details>`

// reviewStateChange reports a GitHub review that approves or requests changes.
// Its body may be empty; the state is the fact the author needs.
func reviewStateChange(e Event) bool {
	return e.Kind == "reviews" && (e.ReviewState == "APPROVED" || e.ReviewState == "CHANGES_REQUESTED")
}

func trustedEvent(e Event, bots []string) bool {
	switch strings.ToUpper(e.Association) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	if bots == nil {
		bots = []string{"coderabbitai", "copilot-pull-request-reviewer"}
	}
	login := strings.TrimSuffix(strings.ToLower(e.Commenter), "[bot]")
	for _, bot := range bots {
		if strings.EqualFold(login, strings.TrimSuffix(strings.ToLower(bot), "[bot]")) {
			return true
		}
	}
	return false
}
