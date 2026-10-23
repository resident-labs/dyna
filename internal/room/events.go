package room

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// EngineSender is the sender of a system event. A caller of PostEvent cannot
// choose it, so a post never looks like it came from a session.
const EngineSender = "dyna"

// EventFamily is the Message.Kind of a system event post.
type EventFamily string

// EventType is the Notice.Kind of a system event post.
type EventType string

// WakeRule says whether an event starts or steers a session.
type WakeRule string

const (
	// WakeNever posts an event without starting or steering any session.
	WakeNever WakeRule = "never"
	// WakeTarget starts or steers the session the event is addressed to, such
	// as the PR author a CI failure belongs to.
	WakeTarget WakeRule = "target"
	// WakeRootIfNeeded starts the root seat only when the post needs root.
	WakeRootIfNeeded WakeRule = "root-if-needed"
)

// Event families and types.
const (
	FamilyWorktree EventFamily = "worktree"
	TypeKept       EventType   = "kept"

	FamilyHook  EventFamily = "hook"
	TypeStarted EventType   = "started"
	TypeResult  EventType   = "result"

	FamilyMerge EventFamily = "merge"
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
)

// mergeSteps lists every merge step the web needs as a union type.
var mergeSteps = []MergeStep{
	MergeQueued, MergeTrial, MergeChecks, MergeLanded, MergeFailed,
	MergeReturned, MergeConflict, MergeRolledBack, MergeDecision,
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

// WorktreeReason is the class of a kept worktree. It keys the dedupe of the
// notice and picks the wording of the card.
type WorktreeReason string

// Worktree reasons.
const (
	WorktreeNotMerged     WorktreeReason = "not-merged"
	WorktreeDirty         WorktreeReason = "dirty"
	WorktreeManualHandoff WorktreeReason = "manual-handoff"
	WorktreeKeepFailed    WorktreeReason = "keep-failed"
	WorktreeOther         WorktreeReason = "other"
)

// worktreeReasons lists every reason the web needs as a union type.
var worktreeReasons = []WorktreeReason{
	WorktreeNotMerged, WorktreeDirty, WorktreeManualHandoff, WorktreeKeepFailed, WorktreeOther,
}

// HookState is how a hook run stands. A running run has just started, and the
// other states end it.
type HookState string

// Hook run states.
const (
	HookRunning HookState = "running"
	HookPassed  HookState = "passed"
	HookFailed  HookState = "failed"
	HookError   HookState = "error"
)

// hookStates lists every state the web needs as a union type.
var hookStates = []HookState{HookRunning, HookPassed, HookFailed, HookError}

// PREvent tells one fact about a tracked change: a CI result, a reviewer
// verdict, a comment, or the end of the change. The watcher posts it to the
// change's author. Every field is typed, so the web card draws the same facts
// the agent text carries and never reads the text.
type PREvent struct {
	Type PRType `json:"type"`
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
	// Verdict is a reviewer verdict: approve, approve-with-nits, changes or question.
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

// EventPayload is the stored payload of a sealed system event. PostEvent
// writes it and the per-event accessors on Message read it, so no code
// outside this package can build the payload of a system post.
type EventPayload struct{ raw []byte }

// IsZero keeps the payload out of a post that has none.
func (p EventPayload) IsZero() bool { return len(p.raw) == 0 }

// MarshalJSON writes the payload as the event struct's own JSON.
func (p EventPayload) MarshalJSON() ([]byte, error) {
	if len(p.raw) == 0 {
		return []byte("null"), nil
	}
	return p.raw, nil
}

// UnmarshalJSON keeps the stored bytes for the event accessor to decode.
func (p *EventPayload) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		p.raw = nil
		return nil
	}
	p.raw = append(p.raw[:0], data...)
	return nil
}

// Event is one fact that the engine posts to a room. Its methods are
// unexported, so only this package declares an event, and PostEvent is the
// only way to store one. The event is the typed payload of the post.
type Event interface {
	eventFamily() EventFamily
	eventType() EventType
	eventSubject() string
	eventDedupe() string
}

// WorktreeEvent tells a session that cleanup kept one of its worktrees, with
// the facts the session needs to act on it. An empty Path means cleanup could
// not keep the session's trees at all.
type WorktreeEvent struct {
	// Owner is the session that owns the tree, or the session being closed
	// when cleanup could not keep the trees.
	Owner  string `json:"owner"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Ahead is the number of commits not on the base. Zero means unknown.
	Ahead  int            `json:"ahead,omitempty"`
	Dirty  []string       `json:"dirty,omitempty"`
	Reason WorktreeReason `json:"reason"`
	// Detail is the full sentence cleanup gave, when it says more than the
	// reason class does.
	Detail string `json:"detail,omitempty"`
}

func (WorktreeEvent) eventFamily() EventFamily { return FamilyWorktree }
func (WorktreeEvent) eventType() EventType     { return TypeKept }
func (w WorktreeEvent) eventSubject() string   { return w.Path }

// eventDedupe keys one post per owner, tree and reason class: a later pass
// that finds the same tree with other counts posts nothing, a new reason does.
func (w WorktreeEvent) eventDedupe() string {
	return w.Owner + "\x00" + w.Path + "\x00" + string(w.Reason)
}

// HookEvent tells a room that a hook run started and how it ended. The two
// types share the payload: a running state is the start, every other state is
// the result. One card per run shows the start folding into the result.
//
// Every start is followed by its own result, so a card never stays running.
// A start posts only where the run already has a room (the event's room or the
// repo's), and a routine run never creates one. A failing alert hook may
// create the repo's room, so the first failure of a repo nobody uses as a room
// shows its result without the start that came before it.
type HookEvent struct {
	// RunID names the stored run. The card opens the run's log with it, so no
	// filesystem path reaches the web.
	RunID string `json:"runId"`
	// Repo is the main checkout whose hook ran, which the log read needs too.
	Repo string `json:"repo"`
	Hook string `json:"hook"`
	// Event is the repo event that started the run.
	Event string `json:"event"`
	// Label is the hook's own label, when it has one.
	Label string `json:"label,omitempty"`
	// Command is the first word of the command only: the full command can
	// hold a secret, so it never leaves the hooks store.
	Command string    `json:"command,omitempty"`
	State   HookState `json:"state"`
	// DurationMS is how long the run took. A start has none.
	DurationMS int `json:"durationMs,omitempty"`
	// Detail is the cause the state cannot carry alone, such as the exit code,
	// or a checkout warning from an install unit.
	Detail string `json:"detail,omitempty"`
}

func (HookEvent) eventFamily() EventFamily { return FamilyHook }

// eventType reads the state: a running run posts the start, any other state
// posts the result.
func (h HookEvent) eventType() EventType {
	if h.State == HookRunning {
		return TypeStarted
	}
	return TypeResult
}

func (h HookEvent) eventSubject() string { return h.RunID }

// eventDedupe keys one post per run and type, so a start and its result are two
// posts that the web folds into one card.
func (h HookEvent) eventDedupe() string {
	return h.RunID + "\x00" + string(h.eventType())
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
	// Reason is the short cause of a failure, a return or a conflict.
	Reason string `json:"reason,omitempty"`
	// Summary is the short facts line the agent text reads, such as a checks
	// count. It never holds raw log lines.
	Summary string `json:"summary,omitempty"`
	// Label names the change a fact is about, as "PR #12". A trial's own
	// events name every member in PRs instead.
	Label string `json:"label,omitempty"`
	// Key is the steward's own identity of this event within a type, such as
	// one checks progress step. One fact per trial needs no key.
	Key string `json:"key,omitempty"`
	// Detail is the evidence the agent reads: CI logs, a root report, or the
	// child URLs a rollback could not restore.
	Detail string `json:"detail,omitempty"`
	// Retries counts the trial rebuilds the steward recorded. Zero means none,
	// because the steward keeps no other retry counter.
	Retries int `json:"retries,omitempty"`
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

// EventSpec is one entry of the system event registry. The web app draws the
// event with Renderer and reads its typed payload; Text is the one-line
// summary that agents and `dyna room log` read.
type EventSpec struct {
	Family   EventFamily
	Type     EventType
	Renderer string
	// Payload is the event struct. The generator in events_test.go reads it to
	// write the TypeScript types, so the two sides cannot drift.
	Payload reflect.Type
	Quiet   bool
	Wake    WakeRule
	// Once drops a later post with the same key instead of appending it.
	Once bool
	Text string
	// TextNoSubject is the summary for an event that names no subject, such
	// as a cleanup failure that has no single tree.
	TextNoSubject string
}

// eventSpecs is the registry of every system event that PostEvent may store.
// A new event needs an entry here, an event struct in this package and a
// renderer in web/src/lib/rooms/eventRenderers.ts. TestEventsFileMatchesRegistry
// writes the web types.
var eventSpecs = []EventSpec{
	{
		Family:   FamilyWorktree,
		Type:     TypeKept,
		Renderer: "WorktreeCard",
		Payload:  reflect.TypeOf(WorktreeEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Worktree kept at {path}: branch {branch}, ahead {ahead}, dirty: {dirty}.",
		// The keep failure names no tree.
		TextNoSubject: "Worktree cleanup could not keep the session's trees.",
	},
	{
		Family:   FamilyHook,
		Type:     TypeStarted,
		Renderer: "HookCard",
		Payload:  reflect.TypeOf(HookEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Hook {hook} started for {event} (run {runId}).",
	},
	{
		Family:   FamilyHook,
		Type:     TypeResult,
		Renderer: "HookCard",
		Payload:  reflect.TypeOf(HookEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Hook {hook} {state} for {event} after {durationMs} ms (run {runId}).",
	},
	// The merge family reports a trial's progress and how it ended. A progress
	// event goes to the member rooms as a quiet card, because the separate PR
	// merged notice wakes each author. A failure, a conflict and a root return
	// reach the author, so agents act on them.
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeQueued),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "Merge trial {trialId} queued for {base} in {repo}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeTrial),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "Merge trial {trialId} started for {base} in {repo}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeChecks),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "Merge trial {trialId} checks: {summary}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeLanded),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "Merge trial {trialId} landed {commit} on {base}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeFailed),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Wake:     WakeTarget,
		Text:     "Merge trial {trialId} failed: {reason}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeReturned),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Wake:     WakeTarget,
		Text:     "Merge trial {trialId} returned by root: {reason}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeConflict),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Wake:     WakeTarget,
		Text:     "Merge conflict for {label} on {base}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeRolledBack),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "Merge rollback for {label} on {base}.",
	},
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeDecision),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeDecisionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "{note}",
	},
	// PR events reach the change's author, but they do not all wake it: the
	// watcher marks an informational notice NoWake, and a notice that falls
	// back to the owner is quiet unless root must act. Every other PR post
	// wakes the session it is addressed to.
	{
		Family:   FamilyPR,
		Type:     EventType(PROpened),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\n{label} opened in {repo}. Review it at {url}",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRReady),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\n{label} is ready to land at {head}.",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRApproved),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\n{label} at {head} approved by its Dyna reviewer:",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRChanges),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\nReviewer requests changes for {label} at {head}:",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRCIFailed),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\nCI failed ({ciAttempt}/{ciMax}): {summary}. Fix the failure and push to this PR.",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRCIPaused),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\n{label} failed CI three times in a row. Later CI failures will not wake the author. Inspect dyna git pr show {url}. Failed jobs: {summary}.",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRCIPassed),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\nCI passed on {label} at {head}.",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRReturned),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\n{label} at {head} was returned to you.",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRMerged),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRClosed),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRComment),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRHeldComment),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRReviewAssigned),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}",
	},
	{
		Family:   FamilyPR,
		Type:     EventType(PRUpdate),
		Renderer: "PRCard",
		Payload:  reflect.TypeOf(PREvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}",
	},
}

// eventEnum is a named string type whose values the web needs as a union.
type eventEnum struct {
	Type   reflect.Type
	Values []string
}

var eventEnums = []eventEnum{
	{Type: reflect.TypeOf(PRType("")), Values: prTypeValues()},
	{Type: reflect.TypeOf(MergeStep("")), Values: mergeStepValues()},
	{Type: reflect.TypeOf(WorktreeReason("")), Values: []string{
		string(WorktreeNotMerged), string(WorktreeDirty), string(WorktreeManualHandoff), string(WorktreeKeepFailed), string(WorktreeOther),
	}},
	{Type: reflect.TypeOf(HookState("")), Values: []string{
		string(HookRunning), string(HookPassed), string(HookFailed), string(HookError),
	}},
}

// eventObjects are the struct types that an event payload embeds. The
// generator writes an interface and a guard for each, so a card reads a typed
// list instead of a stringly field.
var eventObjects = []reflect.Type{
	reflect.TypeOf(MergePR{}),
	reflect.TypeOf(MergeCheckSummary{}),
}

// legacyFamilies are the post kinds that still go through Store.Post with a
// free text. Each later PR seals one family through PostEvent and deletes its
// entry here, until the list is empty.
var legacyFamilies = []string{
	"account",
	"alert",
	"checkin",
	"goal",
	"hierarchy",
	"instructions",
	"issue",
	"notice",
	"seat",
	"spawn",
	"test-health",
	"voice",
}

// agentKinds are the posts a person or an agent writes, and the session turn
// results the engine records for them.
var agentKinds = []string{"message", "completed", "failed", "stopped"}

// EventPost is the registry's description of the post one event makes. notify
// reads it so a poster never chooses the post kind, text, quiet flag or wake.
type EventPost struct {
	Kind  string
	Type  EventType
	Text  string
	Quiet bool
	Wake  WakeRule
}

// DescribeEvent returns the post of an event from the registry.
func DescribeEvent(ev Event) (EventPost, bool) {
	spec, ok := eventSpecFor(string(ev.eventFamily()), string(ev.eventType()))
	if !ok {
		return EventPost{}, false
	}
	text := spec.Text
	if ev.eventSubject() == "" && spec.TextNoSubject != "" {
		text = spec.TextNoSubject
	}
	return EventPost{Kind: string(spec.Family), Type: spec.Type, Text: eventText(text, ev), Quiet: spec.Quiet, Wake: spec.Wake}, true
}

// eventSpecFor returns the registered spec of a family and type.
func eventSpecFor(family, typ string) (EventSpec, bool) {
	for _, spec := range eventSpecs {
		if string(spec.Family) == family && string(spec.Type) == typ {
			return spec, true
		}
	}
	return EventSpec{}, false
}

// sealedFamily reports a family whose posts only PostEvent may store.
func sealedFamily(family string) bool {
	return slices.ContainsFunc(eventSpecs, func(spec EventSpec) bool { return string(spec.Family) == family })
}

// legacyFamily reports a kind that still posts through Store.Post.
func legacyFamily(kind string) bool { return slices.Contains(legacyFamilies, kind) }

// agentKind reports a kind that a person or a session writes.
func agentKind(kind string) bool { return kind == "" || slices.Contains(agentKinds, kind) }

// PostEventInput is one system event to store, with the delivery facts the
// poster knows and the registry does not: who receives it, whether root must
// act, and the session bindings of the recipient addresses.
//
// The registry decides everything else: the post kind, its text, its quiet
// flag and its wake rule. A poster cannot choose them.
type PostEventInput struct {
	RoomID string
	Event  Event
	// ID is the poster's own stable post ID, when it keeps one for retries.
	// A poster without one lets the registry derive it from the event, unless
	// FreshID asks for a new post as Store.Post does.
	ID string
	// FreshID stores a new post for every call, for a poster whose notice has
	// no stable identity of its own.
	FreshID bool
	// To holds the resolved recipient addresses and Via the mention tokens as
	// the poster wrote them.
	To  []string
	Via []string
	// NeedsRoot asks the root seat to act on this post.
	NeedsRoot bool
	// RoomService sends the post as the room's own service, such as the PR
	// watcher. The default sender is the engine.
	RoomService bool
	// SessionIDs binds participant addresses to exact sessions.
	SessionIDs map[string]string
	ReplyTo    string
	// NoWake stores an informational post for the recipient's next turn.
	NoWake bool
}

// PostEvent stores one system event with no recipients but the given ones. It
// is the short form for a quiet event that wakes nobody.
func (s *Store) PostEvent(roomID string, ev Event, to ...string) (Message, error) {
	return s.PostEventInput(PostEventInput{RoomID: roomID, Event: ev, To: to})
}

// PostEventInput stores one system event. It looks up the spec, rejects an
// event whose required fields are empty, fixes the sender to an engine sender,
// derives the post ID from the event so the same event is stored once, and
// applies the spec's quiet and wake rules. A spec with Once drops a later post
// with the same key and other content: it returns no message and no error. A
// retry with identical content returns the stored message, as Post does.
func (s *Store) PostEventInput(in PostEventInput) (Message, error) {
	ev := in.Event
	spec, ok := eventSpecFor(string(ev.eventFamily()), string(ev.eventType()))
	if !ok {
		return Message{}, fmt.Errorf("event %s/%s is not registered in internal/room/events.go", ev.eventFamily(), ev.eventType())
	}
	if err := checkRequired(ev); err != nil {
		return Message{}, err
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return Message{}, err
	}
	// A caller cannot choose the sender of an event post: it is the engine or
	// the room's own service, never a session.
	from := EngineSender
	if in.RoomService {
		rec, err := s.readRecord(in.RoomID)
		if err != nil {
			return Message{}, err
		}
		from = rec.Name
	}
	template := spec.Text
	if ev.eventSubject() == "" && spec.TextNoSubject != "" {
		template = spec.TextNoSubject
	}
	text := eventText(template, ev)
	if spec.Wake == WakeRootIfNeeded && !in.NeedsRoot {
		spec.Wake = WakeNever
	}
	id := in.ID
	if id == "" && !in.FreshID {
		id = eventID(ev)
	}
	msg, err := s.post(in.RoomID, PostInput{
		ID:         id,
		From:       from,
		Kind:       string(spec.Family),
		Notice:     EventNotice{Kind: string(spec.Type)},
		payload:    EventPayload{raw: payload},
		To:         in.To,
		Via:        in.Via,
		NeedsRoot:  in.NeedsRoot,
		SessionIDs: in.SessionIDs,
		ReplyTo:    in.ReplyTo,
		Text:       text,
		Wake:       spec.Wake != WakeNever && !in.NoWake,
	}, true)
	if errors.Is(err, ErrPostIDConflict) && spec.Once {
		return Message{}, nil
	}
	return msg, err
}

// prTypeValues lists the PR types for the generated web union.
func prTypeValues() []string {
	values := make([]string, len(prTypes))
	for i, typ := range prTypes {
		values[i] = string(typ)
	}
	return values
}

// mergeStepValues lists the merge steps for the generated web union.
func mergeStepValues() []string {
	values := make([]string, len(mergeSteps))
	for i, step := range mergeSteps {
		values[i] = string(step)
	}
	return values
}

// eventBlocks are the payload fields that the agent text appends as their own
// paragraph after the spec's one-line template, in this order. A block that the
// event leaves empty adds nothing, so the text has no empty section.
var eventBlocks = []string{"detail", "logTail"}

// eventText renders the spec's one-line template and appends the event's text
// blocks. A block that only repeats the template adds nothing.
func eventText(template string, ev Event) string {
	text := renderEventText(template, ev)
	value := reflect.ValueOf(ev)
	for _, name := range eventBlocks {
		block := eventFieldText(value, name)
		if block == "" || strings.TrimSpace(block) == strings.TrimSpace(text) {
			continue
		}
		text += "\n\n" + block
	}
	return text
}

// checkRequired rejects an event with an empty field that its json tag does
// not mark omitempty.
func checkRequired(ev Event) error {
	value := reflect.ValueOf(ev)
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" || strings.Contains(options, "omitempty") {
			continue
		}
		if value.Field(i).IsZero() {
			return fmt.Errorf("a %s/%s event needs the field %q", ev.eventFamily(), ev.eventType(), name)
		}
	}
	return nil
}

// eventID is the post ID of an event: family, type, subject and the event's
// own dedupe key. The same event in a later pass gets the same ID.
func eventID(ev Event) string {
	key := strings.Join([]string{string(ev.eventFamily()), string(ev.eventType()), ev.eventSubject(), ev.eventDedupe()}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("msg_%x", sum[:8])
}

// eventPlaceholder matches a {field} of a spec's text template. A json name
// can hold capitals, such as {runId}.
var eventPlaceholder = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// renderEventText fills a spec's text template from the event's json fields.
func renderEventText(text string, ev Event) string {
	value := reflect.ValueOf(ev)
	return eventPlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		return eventFieldText(value, m[1:len(m)-1])
	})
}

// eventFieldText formats one json field of an event for the agent text. An
// empty list reads as "none", because a clean tree has no dirty files.
func eventFieldText(value reflect.Value, name string) string {
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName != name {
			continue
		}
		fieldValue := value.Field(i)
		switch fieldValue.Kind() {
		case reflect.String:
			return fieldValue.String()
		case reflect.Int:
			return strconv.FormatInt(fieldValue.Int(), 10)
		case reflect.Slice:
			if fieldValue.Len() == 0 {
				return "none"
			}
			if fieldValue.Type().Elem().Kind() != reflect.String {
				return ""
			}
			items := make([]string, fieldValue.Len())
			for j := range items {
				items[j] = fieldValue.Index(j).String()
			}
			return strings.Join(items, ", ")
		}
	}
	return ""
}

// PR returns the payload of a PR event. It decodes the stored bytes strictly,
// so an unknown field is a failed read rather than a silent mismatch.
func (m Message) PR() (PREvent, bool) {
	if m.Kind != string(FamilyPR) || len(m.Payload.raw) == 0 {
		return PREvent{}, false
	}
	var ev PREvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Type == "" {
		return PREvent{}, false
	}
	return ev, true
}

// Hook returns the payload of a hook run event, of either type. It decodes the
// stored bytes strictly, so an unknown field is a failed read rather than a
// silent mismatch.
func (m Message) Hook() (HookEvent, bool) {
	if m.Kind != string(FamilyHook) || len(m.Payload.raw) == 0 {
		return HookEvent{}, false
	}
	if m.Notice.Kind != string(TypeStarted) && m.Notice.Kind != string(TypeResult) {
		return HookEvent{}, false
	}
	var ev HookEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return HookEvent{}, false
	}
	return ev, true
}

// Worktree returns the payload of a kept-worktree event. It decodes the stored
// bytes strictly, so an unknown field is a failed read rather than a silent
// mismatch.
func (m Message) Worktree() (WorktreeEvent, bool) {
	if m.Kind != string(FamilyWorktree) || m.Notice.Kind != string(TypeKept) || len(m.Payload.raw) == 0 {
		return WorktreeEvent{}, false
	}
	var ev WorktreeEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return WorktreeEvent{}, false
	}
	return ev, true
}

// Merge returns the payload of a merge trial event. It decodes the stored bytes
// strictly, so an unknown field is a failed read rather than a silent mismatch.
func (m Message) Merge() (MergeEvent, bool) {
	if m.Kind != string(FamilyMerge) || m.Notice.Kind == string(MergeDecision) || len(m.Payload.raw) == 0 {
		return MergeEvent{}, false
	}
	var ev MergeEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Type == "" {
		return MergeEvent{}, false
	}
	return ev, true
}

// MergeDecision returns the payload of a recorded merge decision. It decodes the
// stored bytes strictly, so an unknown field is a failed read rather than a
// silent mismatch.
func (m Message) MergeDecision() (MergeDecisionEvent, bool) {
	if m.Kind != string(FamilyMerge) || m.Notice.Kind != string(MergeDecision) || len(m.Payload.raw) == 0 {
		return MergeDecisionEvent{}, false
	}
	var ev MergeDecisionEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return MergeDecisionEvent{}, false
	}
	return ev, true
}
