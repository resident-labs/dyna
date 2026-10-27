package room

// MergeStep is the step of a merge trial that a merge event reports, or the
// steward's recorded decision about the queue. A trial reports its progress
// and how it ended; the web card picks its chip and its progress bar from it.
type MergeStep string

// Merge steps and the recorded decision.
const (
	MergeQueued     MergeStep = "queued"
	MergeTrial      MergeStep = "trial"
	MergeChecks     MergeStep = "checks"
	MergeLanded     MergeStep = "landed"
	MergeFailed     MergeStep = "failed"
	MergeReturned   MergeStep = "returned"
	MergeConflict   MergeStep = "conflict"
	MergeRolledBack MergeStep = "rolled-back"
	// MergeDecision is root's recorded decision about the queue, not a trial
	// step: it has no trial and its own payload.
	MergeDecision MergeStep = "decision"
	// MergeFixRequested is the owner's or root's request to merge one approved,
	// green PR while main CI is red. The engine records it, so it is a typed
	// event rather than a post in the owner's name.
	MergeFixRequested MergeStep = "fix-requested"
)

// mergeSteps lists every merge step the web needs as a union type.
var mergeSteps = []MergeStep{
	MergeQueued, MergeTrial, MergeChecks, MergeLanded, MergeFailed,
	MergeReturned, MergeConflict, MergeRolledBack, MergeDecision, MergeFixRequested,
}

// MergeReasonKind is the class of a merge event's cause. The card words a
// cause from this kind, so a reason the steward generates itself never repeats
// the evidence the event already carries.
type MergeReasonKind string

// Merge reason kinds.
const (
	// MergeChecksFailed is a trial whose CI failed. Its failing check names are
	// in Checks, so the event carries no reason text of its own.
	MergeChecksFailed MergeReasonKind = "checks-failed"
	// MergeConflict is a change that conflicts with another member or the base.
	MergeConflictReason MergeReasonKind = "conflict"
	// MergeRootReturn is root's recorded decision to return the change.
	MergeRootReturn MergeReasonKind = "root-return"
	// MergeTestedHead is a single PR whose head contains the base tip and whose
	// required CI passed at that head, so its own tree needs no merge trial.
	MergeTestedHead MergeReasonKind = "tested-head"
	// MergeReasonOther is any other cause, which the event words in Reason.
	MergeReasonOther MergeReasonKind = "other"
)

// mergeReasonKinds lists every reason kind the web needs as a union type.
var mergeReasonKinds = []MergeReasonKind{
	MergeChecksFailed, MergeConflictReason, MergeRootReturn, MergeTestedHead, MergeReasonOther,
}

// MergePR is one member PR of a merge trial, as the card names it.
type MergePR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// MergeCheckSummary is the CI summary of a merge trial. It never holds raw logs: the
// agent text keeps those. Done counts the checks that finished; passed and
// failed checks are both finished, and only a failed trial names its failures.
type MergeCheckSummary struct {
	Done   int      `json:"done"`
	Total  int      `json:"total,omitempty"`
	Failed []string `json:"failed,omitempty"`
}

// MergeEvent tells how one merge trial stands: its member PRs, its CI, and the
// commit it landed. Every event of a trial folds into one card by TrialID, so
// the trial reports its progress and its end on the same card.
type MergeEvent struct {
	Type MergeStep `json:"type"`
	// TrialID is the fold and dedupe subject: the trial branch, or the change
	// a fact belongs to when no trial exists.
	TrialID string `json:"trialId"`
	Repo    string `json:"repo"`
	// Base is the branch the trial merges into.
	Base string    `json:"base"`
	PRs  []MergePR `json:"prs"`
	// Checks is the CI summary, when the steward has it.
	Checks *MergeCheckSummary `json:"checks,omitempty"`
	// Commit is the commit the trial landed on the base branch.
	Commit string `json:"commit,omitempty"`
	// Reason is the short cause of a failure, a return or a conflict. A cause
	// the card can word from ReasonKind stays empty here, so the card never
	// says the same thing twice.
	Reason string `json:"reason,omitempty"`
	// ReasonKind is the class of the cause, when the steward generated it.
	// Root's own text keeps MergeReasonOther and shows in Reason.
	ReasonKind MergeReasonKind `json:"reasonKind,omitempty"`
	// Summary is the short facts line the agent text reads, such as a checks
	// count. It never holds raw log lines.
	Summary string `json:"summary,omitempty"`
	// Label names the change a fact is about, as "PR #12". A trial's own
	// events name every member in PRs instead.
	Label string `json:"label,omitempty"`
	// Member is the PR number a fact is about, when the fact names one member
	// of a batch rather than the whole trial: a return, a conflict or a failure
	// that holds back that member alone. A trial-wide fact leaves it zero, and
	// the card then names the whole batch.
	Member int `json:"member,omitempty"`
	// Key is the steward's own identity of this event within a type, such as
	// one checks progress step. One fact per trial needs no key.
	Key string `json:"key,omitempty"`
	// Head is the commit a merge-fix request was made for. The merge itself
	// names the commit it landed in Commit.
	Head string `json:"head,omitempty"`
	// Actor is who asked for a merge-fix, as the card names them: the room
	// owner or the root seat.
	Actor string `json:"actor,omitempty"`
	// Detail is the evidence the agent reads: CI logs, a root report, or the
	// child URLs a rollback could not restore.
	Detail string `json:"detail,omitempty"`
}

func (MergeEvent) eventFamily() EventFamily { return FamilyMerge }
func (m MergeEvent) eventType() EventType   { return EventType(m.Type) }

// eventSubject folds every event of one trial into one card.
func (m MergeEvent) eventSubject() string { return m.TrialID }

// eventDedupe keys one post per fact of a trial. The type and the steward's
// own key make a queued trial post once and each checks step post once.
func (m MergeEvent) eventDedupe() string {
	return string(m.Type) + "\x00" + m.Key
}

// MergeDecisionEvent is root's recorded decision about the merge queue. It has
// no trial: each decision is its own post and its own card.
type MergeDecisionEvent struct {
	// Note is the one line root recorded.
	Note string `json:"note"`
}

func (MergeDecisionEvent) eventFamily() EventFamily { return FamilyMerge }
func (MergeDecisionEvent) eventType() EventType     { return EventType(MergeDecision) }

// eventSubject keeps a decision out of any trial's card.
func (MergeDecisionEvent) eventSubject() string { return "decision" }

// eventDedupe keys a decision by its own line. Each decision posts as a new
// event with a fresh ID, so two equal notes stay two decisions.
func (d MergeDecisionEvent) eventDedupe() string { return d.Note }
