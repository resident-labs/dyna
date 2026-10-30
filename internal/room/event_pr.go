package room

import (
	"strings"
)

// The PR family posts one event per fact the watcher observed about a change.
// Its events wake the change's author, so unlike the quiet families their texts
// are input an agent acts on.
const FamilyPR EventFamily = "pr"

// PRType is the fact a PR event reports. The web card picks its chip from it.
type PRType string

// PR event types. The CI attempt and the reviewer verdict carry their own
// fields, so a card never reads the text to tell them apart.
const (
	PROpened         PRType = "opened"
	PRReady          PRType = "ready"
	PRApproved       PRType = "approved"
	PRChanges        PRType = "changes"
	PRCIFailed       PRType = "ci-failed"
	PRCIPaused       PRType = "ci-paused"
	PRCIPassed       PRType = "ci-passed"
	PRReturned       PRType = "returned"
	PRMerged         PRType = "merged"
	PRClosed         PRType = "closed"
	PRComment        PRType = "comment"
	PRHeldComment    PRType = "held-comment"
	PRReviewAssigned PRType = "review-assigned"
	// PRUpdate is a change fact that none of the other types names, such as a
	// CI budget note or a merge report that PR 3b will type.
	PRUpdate PRType = "update"
)

// prTypes lists every PR type the web needs as a union type.
var prTypes = []PRType{
	PROpened, PRReady, PRApproved, PRChanges, PRCIFailed, PRCIPaused, PRCIPassed,
	PRReturned, PRMerged, PRClosed, PRComment, PRHeldComment, PRReviewAssigned, PRUpdate,
}

type PROutcome string

const (
	PROutcomeOK     PROutcome = "ok"
	PROutcomeFailed PROutcome = "failed"
	PROutcomeInfo   PROutcome = "info"
)

var prOutcomes = []PROutcome{PROutcomeOK, PROutcomeFailed, PROutcomeInfo}

// PREvent tells one fact about a tracked change: a CI result, a reviewer
// verdict, a comment, or the end of the change. The watcher posts it to the
// change's author. Every field is typed, so the web card draws the same facts
// the agent text carries and never reads the text.
type PREvent struct {
	Type    PRType    `json:"type"`
	Outcome PROutcome `json:"outcome,omitempty"`
	// Label names the change in the agent text, as "PR #12" or "Change <key>".
	Label string `json:"label"`
	// Location is where the change lives: its URL, or the watcher's key of a
	// local change. It heads the agent text, as the watcher used to write it.
	Location string `json:"location"`
	// Number is the GitHub PR number. A change on a local branch has none.
	Number int `json:"number,omitempty"`
	// ChangeID is the watcher's key of the change, such as the PR URL.
	ChangeID string `json:"changeId"`
	// Key is the watcher's own identity of this notice, such as its write
	// token. Two comments on one head need it to post as two events.
	Key       string `json:"key,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Title     string `json:"title,omitempty"`
	Head      string `json:"head,omitempty"`
	URL       string `json:"url,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CIAttempt int    `json:"ciAttempt,omitempty"`
	CIMax     int    `json:"ciMax,omitempty"`
	// Summary is the short facts line: the failed check names, the reviewer's
	// verdict summary. It never holds raw log lines.
	Summary   string `json:"summary,omitempty"`
	Commenter string `json:"commenter,omitempty"`
	CommentID string `json:"commentId,omitempty"`
	// Verdict is a reviewer verdict: approve, changes or stop-early, or one of
	// the values stored before the typed results.
	Verdict string `json:"verdict,omitempty"`
	// Detail is the body the agent reads: review markdown, comment text or the
	// remaining notice sentences.
	Detail string `json:"detail,omitempty"`
	// LogTail is the labelled CI log tail block, when the watcher had one.
	LogTail string `json:"logTail,omitempty"`
	// Dropped counts the comments the watcher left out as noise.
	Dropped int `json:"dropped,omitempty"`
}

func (p PREvent) eventFamily() EventFamily { return FamilyPR }
func (p PREvent) eventType() EventType     { return EventType(p.Type) }

// eventSubject is the change, so every event of one change folds into one card.
func (p PREvent) eventSubject() string { return p.ChangeID }

// eventDedupe keys one post per type and head. A comment adds its own key and
// comment ID, because one head can get many comments. The key is the watcher's
// notice token, so a retried notice keeps its post ID.
func (p PREvent) eventDedupe() string {
	return strings.Join([]string{string(p.Type), p.Head, p.CommentID, p.Key}, "\x00")
}

// eventThread groups only lifecycle facts. Comments and other actionable facts stay separate.
func (p PREvent) eventThread() string {
	switch p.Type {
	case PRUpdate, PRReady, PRCIFailed, PRCIPassed, PRMerged, PRClosed:
		if p.ChangeID != "" {
			return "pr:" + p.ChangeID
		}
	}
	return ""
}
