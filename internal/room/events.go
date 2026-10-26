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

// EventProducer names the engine service or worker that observed an event. The
// producer is data on the post, so a card can name where a fact came from
// without inventing a sender.
//
// TODO: add the remaining producers when their families are typed: the
// doctor, the engine, CI health, the merge steward and the PR watcher.
type EventProducer string

// Producers.
const (
	// ProducerParentCheckin is the scheduler that reminds a parent to look at
	// its children.
	ProducerParentCheckin EventProducer = "parent-checkin"
	// ProducerDoctor is the machine doctor that reports what it found.
	ProducerDoctor EventProducer = "doctor"
	// ProducerEngine is the engine itself, as when migration keeps it
	// read-only and it tells every room so.
	ProducerEngine EventProducer = "engine"
	// ProducerLibrarian is the librarian service.
	ProducerLibrarian EventProducer = "librarian"
	// ProducerReviewer is a reviewer service.
	ProducerReviewer EventProducer = "reviewer"
	// ProducerHelper is a helper service.
	ProducerHelper EventProducer = "helper"
	// ProducerShell is the supervisor that watches a detached shell job.
	ProducerShell EventProducer = "shell"
	// ProducerWorkflow is the workflow runtime that ran a detached run.
	ProducerWorkflow EventProducer = "workflow"
)

// Audience is who an event's post belongs to. A room event is part of the
// room's own conversation. A session event belongs to the one session it is
// addressed to, and lives in that session's DM.
type Audience string

// Audiences.
const (
	AudienceRoom    Audience = "room"
	AudienceSession Audience = "session"
)

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
	// WakeRoot always starts the root seat, because the event needs root to
	// act now. Only an event addressed to the root seat may use it.
	WakeRoot WakeRule = "root"
)

// Event families and types.
const (
	FamilyWorktree EventFamily = "worktree"
	TypeKept       EventType   = "kept"

	// The shell family reports how a detached shell job ended.
	FamilyShell EventFamily = "shell"
	// The run family reports how a detached workflow run ended.
	FamilyRun EventFamily = "run"
	// The turn family reports how a session turn ended.
	FamilyTurn EventFamily = "turn"

	// TypeTurnCompleted, TypeTurnFailed and TypeTurnStopped are the states of a
	// finished turn. A shell and a run post their one type, TypeResult, with
	// the state in their own payload.
	TypeTurnCompleted EventType = "completed"
	TypeTurnFailed    EventType = "failed"
	TypeTurnStopped   EventType = "stopped"

	FamilyHook  EventFamily = "hook"
	TypeStarted EventType   = "started"
	TypeResult  EventType   = "result"

	FamilyMerge EventFamily = "merge"

	FamilyGoal     EventFamily = "goal"
	FamilyCheckin  EventFamily = "checkin"
	FamilyCIHealth EventFamily = "ci-health"
	FamilyRule     EventFamily = "rule"
	FamilySession  EventFamily = "session"
	FamilyAlert    EventFamily = "alert"
	FamilyService  EventFamily = "service"
	FamilyEngine   EventFamily = "engine"
	FamilyQuestion EventFamily = "question"

	// TypeSummary tells a check-in apart from any other event of its family.
	TypeSummary EventType = "summary"
	// TypeInstructionBatch is one owner-facing summary of a shared instruction
	// change. Each session still gets its own agent-facing note.
	TypeInstructionBatch EventType = "instruction-batch"
	// TypeSlowTest is the CI health line about a test over the limit.
	TypeSlowTest EventType = "slow-test"
	// TypeChanged is a rule that someone added, edited or removed.
	TypeChanged EventType = "changed"

	FamilyMachine EventFamily = "machine"
	TypeLowDisk   EventType   = "low-disk"
	// TypeCacheRepaired is a run that switched linked managed cache paths back
	// to real directories.
	TypeCacheRepaired EventType = "cache-repaired"

	// FamilyCall reports how the owner answered a call request. The engine
	// posts it, so the answer wears no invented voice and carries no raw ID in
	// its text.
	FamilyCall EventFamily = "call"

	// TypeRootFlush is the engine's nudge that delivers the quiet posts a room
	// held for its root seat.
	TypeRootFlush EventType = "root-flush"
	// TypeWithdrawn is a question that was withdrawn before it was answered.
	TypeWithdrawn EventType = "withdrawn"
	// FamilyReview reports how one reviewer turn ended. A review result is its
	// own post, so the room reads a verdict, its findings and a stop reason as
	// typed facts instead of parsing review text.
	FamilyReview EventFamily = "review"
)

// The issue family posts one event per fact about a room issue. Every filer
// ends in room.Store.FileIssue, so the librarian's issue, an agent's and the
// owner's look the same and differ only in who filed them.
const FamilyIssue EventFamily = "issue"

// IssueChange is what happened to an issue. The web card picks its state chip
// from the event, never from the text.
type IssueChange string

// Issue changes.
const (
	IssueFiled   IssueChange = "filed"
	IssueUpdated IssueChange = "updated"
	IssueClosed  IssueChange = "closed"
)

// issueChanges lists every issue change the web needs as a union type.
var issueChanges = []IssueChange{IssueFiled, IssueUpdated, IssueClosed}

// IssueState is where an issue stands after the event. It is the state the
// room store records, so the card and the Issues panel cannot disagree.
type IssueState string

// Issue states.
const (
	IssueStateOpen     IssueState = "open"
	IssueStateGoal     IssueState = "goal"
	IssueStateExported IssueState = "exported"
	IssueStateClosed   IssueState = "closed"
)

// issueStates lists every issue state the web needs as a union type.
var issueStates = []IssueState{IssueStateOpen, IssueStateGoal, IssueStateExported, IssueStateClosed}

// Valid reports whether the state is one the protocol defines.
func (s IssueState) Valid() bool {
	for _, known := range issueStates {
		if s == known {
			return true
		}
	}
	return false
}

// IssueActor is who caused an issue change: the engine itself, or the address
// of the session that did. The engine is not a session, so a change the engine
// makes names itself here instead of borrowing a session's name.
type IssueActor string

// ActorEngine is the engine itself, as when the PR watcher closes an issue
// because the PR that fixed it merged.
const ActorEngine IssueActor = "engine"

// String names the actor in a sentence: a session address as it is, and the
// engine in words, because "engine closed issue D-7" reads badly. The agent
// text of a system event renders a named string type through String.
func (a IssueActor) String() string {
	if a == ActorEngine {
		return "The engine"
	}
	return string(a)
}

// IssueFile is one place an issue points at. A line of zero means the whole
// file.
type IssueFile struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// IssueEvent tells the room that an issue was filed or changed. It carries
// every fact the card shows, so the card never reads the post text.
//
// A new issue is a backlog item: the event wakes nobody, root included. An
// update or a close is equally quiet and reaches the owner feed only.
type IssueEvent struct {
	Type IssueChange `json:"type"`
	// IssueID is empty at filing time: the issue's ID is the post's own ID.
	IssueID string      `json:"issueId,omitempty"`
	Number  int         `json:"number"`
	Title   string      `json:"title"`
	Files   []IssueFile `json:"files,omitempty"`
	Symbols []string    `json:"symbols,omitempty"`
	// Cost and Direction are the librarian's short note fields.
	Cost      string     `json:"cost,omitempty"`
	Direction string     `json:"direction,omitempty"`
	State     IssueState `json:"state"`
	Filer     string     `json:"filer"`
	// Actor is who caused this change: the filer for a filing, the closer for
	// a close. An automatic close names the engine, never the session whose
	// authority the engine used. It is empty when the post names nobody else.
	Actor    IssueActor   `json:"actor,omitempty"`
	Assignee string       `json:"assignee,omitempty"`
	ClosedBy *CloseOrigin `json:"closedBy,omitempty"`
	Reason   string       `json:"reason,omitempty"`
	// Detail is the note body an agent reads: the filed note. A close reason is
	// one line, and the close text already carries it.
	Detail string `json:"detail,omitempty"`
}

func (IssueEvent) eventFamily() EventFamily { return FamilyIssue }
func (p IssueEvent) eventType() EventType   { return EventType(p.Type) }

// eventSubject is the issue's room number, so every event of one issue folds
// into one card.
func (p IssueEvent) eventSubject() string { return "D-" + strconv.Itoa(p.Number) }

// eventDedupe keys one post per issue and change: a retried filing keeps its
// post, and a second close of the same issue posts nothing.
func (p IssueEvent) eventDedupe() string {
	return string(p.Type) + "\x00" + string(p.State)
}

// GoalChange is what happened to a goal. The web row reads it instead of
// parsing the post text.
type GoalChange string

// Goal changes.
const (
	GoalAdded   GoalChange = "added"
	GoalEdited  GoalChange = "edited"
	GoalAmended GoalChange = "amended"
	// GoalItemsDone tells a goal's holder that a session closed the goal's last
	// open item. Only the holder ends the goal itself.
	GoalItemsDone GoalChange = "items_done"
	GoalFinished  GoalChange = "finished"
	GoalDropped   GoalChange = "dropped"
)

// goalChanges lists every goal change the web needs as a union type.
var goalChanges = []GoalChange{GoalAdded, GoalEdited, GoalAmended, GoalItemsDone, GoalFinished, GoalDropped}

// GoalState is where the goal stands after the event.
type GoalState string

// Goal states.
const (
	GoalStateActive  GoalState = "active"
	GoalStateDone    GoalState = "done"
	GoalStateDropped GoalState = "dropped"
)

// goalStates lists every goal state the web needs as a union type.
var goalStates = []GoalState{GoalStateActive, GoalStateDone, GoalStateDropped}

// GoalEvent tells the owner feed that a goal changed. The owner feed is its
// only recipient, so it wakes nobody.
type GoalEvent struct {
	Type   GoalChange `json:"type"`
	GoalID string     `json:"goalId"`
	// Title is the goal text, clipped as the feed row needs it.
	Title string    `json:"title"`
	Actor string    `json:"actor"`
	State GoalState `json:"state"`
	// GoalItemsDone records how many times the goal's last open item closed.
	// It is part of the post identity, so a reopen and a second close notify
	// the holders again while a retried post keeps the post it made.
	ItemsClosed int `json:"itemsClosed,omitempty"`
	// Summary is the finishing note of a finished goal, or the text of a brief
	// amendment.
	Summary string `json:"summary,omitempty"`
	// Amendment is how many amendments the goal holds after this one, oldest
	// first. It is part of the post identity, so the same words amended twice
	// still deliver twice.
	Amendment int `json:"amendment,omitempty"`
}

func (GoalEvent) eventFamily() EventFamily { return FamilyGoal }
func (g GoalEvent) eventType() EventType   { return EventType(g.Type) }

func (g GoalEvent) eventSubject() string { return g.GoalID }

// eventDedupe keys one post per goal and change and text, so a retried edit
// posts once and a second edit posts again. An amendment adds its stored
// position and a closed checklist adds its close number: two posts with the
// same words are two different posts.
func (g GoalEvent) eventDedupe() string {
	key := strings.Join([]string{string(g.Type), g.Title, g.Summary, strconv.Itoa(g.Amendment)}, "\x00")
	if g.ItemsClosed > 0 {
		key += "\x00" + strconv.Itoa(g.ItemsClosed)
	}
	return key
}

// CheckinState is a child's activity when the check-in was written.
type CheckinState string

// Check-in states. They are the session states a parent acts on.
const (
	CheckinRunning CheckinState = "running"
	CheckinQueued  CheckinState = "queued"
	CheckinIdle    CheckinState = "idle"
)

// checkinStates lists every check-in state the web needs as a union type.
var checkinStates = []CheckinState{CheckinRunning, CheckinQueued, CheckinIdle}

// CheckinCounts is how many children were in each state.
type CheckinCounts struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Idle    int `json:"idle"`
}

// CheckinChild is one child in a check-in, with the facts a parent acts on.
type CheckinChild struct {
	Name  string       `json:"name"`
	State CheckinState `json:"state"`
	// Age is how long the child has been in this state, as "12m" or "<1m".
	Age string `json:"age"`
	// Goal is the work the child holds, or "no open goal or item".
	Goal string `json:"goal"`
	// PR names the child's open pull requests, or "no open PRs".
	PR string `json:"pr"`
}

// CheckinEvent is a parent check-in: the stalest children first, so the
// parent reads the ones most likely to be stuck before the rest.
//
// It wakes the parent and no one else, as the free-text check-in did.
type CheckinEvent struct {
	Parent string `json:"parent"`
	// WindowStart is when the check-in window opened, in unix milliseconds.
	// It keys the post of a window, so a retried delivery posts once.
	WindowStart int64          `json:"windowStart"`
	Counts      CheckinCounts  `json:"counts"`
	Children    []CheckinChild `json:"children"`
	// Detail is the child table the agent reads, one row per child.
	Detail string `json:"detail,omitempty"`
}

func (CheckinEvent) eventFamily() EventFamily { return FamilyCheckin }
func (CheckinEvent) eventType() EventType     { return TypeSummary }
func (c CheckinEvent) eventSubject() string   { return c.Parent }

func (c CheckinEvent) eventDedupe() string { return strconv.FormatInt(c.WindowStart, 10) }

// SlowTestEvent reports the tests that newly crossed the CI limit. It is a
// quiet owner-feed line; the PR watcher files it and nobody wakes.
type SlowTestEvent struct {
	// Run is the CI run token, such as "1241/2".
	Run string `json:"run"`
	// Count is how many tests crossed the limit in this run.
	Count     int                `json:"count"`
	Crossings []SlowTestCrossing `json:"crossings"`
	// Detail is the one line per crossing that the room log reads.
	Detail string `json:"detail,omitempty"`
}

// SlowTestCrossing is one test that crossed the limit, with both of its times.
type SlowTestCrossing struct {
	Test       string   `json:"test"`
	Package    string   `json:"package,omitempty"`
	Features   []string `json:"features,omitempty"`
	DurationMS int      `json:"durationMs"`
	Run        string   `json:"run"`
	OtherMS    int      `json:"otherMs"`
	OtherRun   string   `json:"otherRun"`
	LimitMS    int      `json:"limitMs"`
}

func (SlowTestEvent) eventFamily() EventFamily { return FamilyCIHealth }
func (SlowTestEvent) eventType() EventType     { return TypeSlowTest }
func (s SlowTestEvent) eventSubject() string   { return s.Run }
func (s SlowTestEvent) eventDedupe() string    { return s.Run }

// RuleVerb is what happened to a rule.
type RuleVerb string

// Rule verbs.
const (
	RuleAdded   RuleVerb = "added"
	RuleEdited  RuleVerb = "edited"
	RuleRemoved RuleVerb = "removed"
)

// RuleEvent tells the owner feed that the room's rules changed. It replaces
// the free-text notice the rule commands used to post.
type RuleEvent struct {
	RuleID string `json:"ruleId"`
	// Scope is "room" or "global".
	Scope string   `json:"scope"`
	Actor string   `json:"actor"`
	Verb  RuleVerb `json:"verb"`
	// Text is the rule's own text, empty when it was removed.
	Text string `json:"text,omitempty"`
	// Sessions lists the open sessions whose instruction note carries this
	// change. The rule row shows one reach count instead of one row per session.
	Sessions []string `json:"sessions,omitempty"`
}

func (RuleEvent) eventFamily() EventFamily { return FamilyRule }
func (RuleEvent) eventType() EventType     { return TypeChanged }
func (r RuleEvent) eventSubject() string   { return r.RuleID }
func (r RuleEvent) eventDedupe() string    { return string(r.Verb) }

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
	// hold a secret, so it never leaves the hooks store. A built-in run has
	// no command, because the engine runs its own pass.
	Command string    `json:"command,omitempty"`
	State   HookState `json:"state"`
	// Builtin marks a run of an automatic hook, so the card names the pass
	// instead of a command.
	Builtin bool `json:"builtin,omitempty"`
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

// MachineEvent tells a room's root that a machine stopped taking CI work. It
// carries the sizes the probe read and the floor it missed, so the card says
// what to free without reading the post text.
type MachineEvent struct {
	Name string `json:"name"`
	// FreeBytes and FloorBytes are the free space of the machine's /var/tmp and
	// the floor that free space missed.
	FreeBytes  int `json:"freeBytes"`
	FloorBytes int `json:"floorBytes"`
	// Summary is the short facts line the agent text reads: the two sizes as a
	// person reads them.
	Summary string `json:"summary"`
	// AtMS is when Dyna posted the alert, in milliseconds since the Unix epoch.
	// Two low-disk periods of one machine are two alerts, because each has its
	// own time.
	AtMS int `json:"atMs"`
}

func (MachineEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineEvent) eventType() EventType     { return TypeLowDisk }

// eventSubject is the machine, so the alerts of one machine fold into one card.
func (m MachineEvent) eventSubject() string { return m.Name }

// eventDedupe keys one alert per machine and probe, so a retried alert of the
// same transition posts once and a later period posts again.
func (m MachineEvent) eventDedupe() string {
	return strconv.Itoa(m.AtMS)
}

// MachineCacheEvent tells a room that a run switched linked CI caches back to
// managed real directories. It names the machine and the cache kinds in words,
// never a path or a link target, which belong to the machine and not the room.
type MachineCacheEvent struct {
	Name string `json:"name"`
	// Caches names each repaired cache in clear words, such as "Go build
	// cache".
	Caches []string `json:"caches"`
	// Summary is the short facts line the agent text reads.
	Summary string `json:"summary"`
	// AtMS is when Dyna repaired the caches, in milliseconds since the Unix
	// epoch. Each repair is its own post, because each has its own time.
	AtMS int `json:"atMs"`
}

func (MachineCacheEvent) eventFamily() EventFamily { return FamilyMachine }
func (MachineCacheEvent) eventType() EventType     { return TypeCacheRepaired }

// eventSubject is the machine, so repairs of one machine fold into one card.
func (m MachineCacheEvent) eventSubject() string { return m.Name }

// eventDedupe keys one repair per machine and time, so a retried post of the
// same repair posts once.
func (m MachineCacheEvent) eventDedupe() string { return strconv.Itoa(m.AtMS) }

// CallState is how the owner answered a call request: they joined it, they
// declined it, or the request was withdrawn before they answered.
type CallState string

// Call states.
const (
	CallJoined    CallState = "joined"
	CallDeclined  CallState = "declined"
	CallWithdrawn CallState = "withdrawn"
)

// callStates lists every call state the web needs as a union type.
var callStates = []CallState{CallJoined, CallDeclined, CallWithdrawn}

// CallEvent tells the room how one call request ended. It carries the
// request's ID, so the card names the call without reading the post text.
type CallEvent struct {
	Type CallState `json:"type"`
	// Call is the request ID the owner's call store made.
	Call string `json:"call"`
}

func (CallEvent) eventFamily() EventFamily { return FamilyCall }
func (c CallEvent) eventType() EventType   { return EventType(c.Type) }

// eventSubject is the request, so the answer of one call is one row.
func (c CallEvent) eventSubject() string { return c.Call }

// eventDedupe keys one post per request and answer: a retried close delivery
// posts once, and a request that rings again is its own post.
func (c CallEvent) eventDedupe() string { return string(c.Type) + "\x00" + c.Call }

// SessionChange is what a session note reports about its subject.
type SessionChange string

// Session changes.
const (
	// SessionRenamed is a session that took a new name.
	SessionRenamed SessionChange = "renamed"
	// SessionPromoted is a session that moved to the top level.
	SessionPromoted SessionChange = "promoted"
	// SessionAdopted is a session that moved under another session.
	SessionAdopted SessionChange = "adopted"
	// SessionAccount is a turn that moved to another account or profile.
	SessionAccount SessionChange = "account"
	// SessionInstructions is a standing-instruction change that a session
	// must apply from its next turn.
	SessionInstructions SessionChange = "instructions"
	// SessionSeat is a change of who holds the room's root seat.
	SessionSeat SessionChange = "seat"
	// SessionSpawned is a session the owner started from the composer.
	SessionSpawned SessionChange = "spawned"
	// SessionTransfer asks a session for the handoff a model transfer needs.
	SessionTransfer SessionChange = "transfer-request"
)

// sessionChanges lists every session change the web needs as a union type.
var sessionChanges = []SessionChange{
	SessionRenamed, SessionPromoted, SessionAdopted, SessionAccount,
	SessionInstructions, SessionSeat, SessionSpawned, SessionTransfer,
}

// AccountReason is why a turn moved to another account or profile.
type AccountReason string

// Account reasons.
const (
	// AccountUsageLimit is an account that reached its provider limit.
	AccountUsageLimit AccountReason = "usage-limit"
	// AccountFallback is a profile that reached its provider limit, whose
	// session moved to the profile's fallback.
	AccountFallback AccountReason = "fallback"
)

// accountReasons lists every account reason the web needs as a union type.
var accountReasons = []AccountReason{AccountUsageLimit, AccountFallback}

// InstructionAction is what happened to one section of the standing
// instructions. It picks the wording of the note's row.
type InstructionAction string

// Instruction actions.
const (
	// InstructionItems is a section whose tracked lines changed.
	InstructionItems InstructionAction = "items"
	// InstructionBody is a section whose whole text changed.
	InstructionBody InstructionAction = "body"
	// InstructionRemoved is a section that is gone.
	InstructionRemoved InstructionAction = "removed"
)

// instructionActions lists every instruction action the web needs as a union.
var instructionActions = []InstructionAction{InstructionItems, InstructionBody, InstructionRemoved}

// InstructionChange is one section that a standing-instruction note names.
type InstructionChange struct {
	Name   string            `json:"name"`
	Action InstructionAction `json:"action"`
	// Added and Removed are the tracked lines the section gained and lost.
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Body is the section's new text, for a whole-text change.
	Body string `json:"body,omitempty"`
}

// SessionEvent is one note about a session: a rename, a move, an account
// change, a standing-instruction change, a root-seat change or a spawn. Every
// field is typed, so the note's row and the agent text carry the same facts.
//
// Session is the address of the session the note is about. A seat change is
// about the room's root seat, so it names the room.
type SessionEvent struct {
	Type    SessionChange `json:"type"`
	Session string        `json:"session"`
	// Name and OldName are the session's name before and after a rename.
	Name    string `json:"name,omitempty"`
	OldName string `json:"oldName,omitempty"`
	// From and To are the parents a move left and joined, or the account and
	// profile an account change left and joined. "top level" means no parent.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Reason is why an account change happened.
	Reason AccountReason `json:"reason,omitempty"`
	// Profile is the profile a spawned session runs on, or a transfer's target.
	Profile string `json:"profile,omitempty"`
	// Transfer is the transfer ID a handoff request names, so the card can
	// point at the transfer that asked for it.
	Transfer string `json:"transfer,omitempty"`
	// Dir is the directory a spawned session works in.
	Dir string `json:"dir,omitempty"`
	// Seat is the label of a root-seat change.
	Seat string `json:"seat,omitempty"`
	// Sections lists the instruction sections that changed.
	Sections []InstructionChange `json:"sections,omitempty"`
	// Occurrence counts the notes about one kind of change, so a change that
	// reverts to an earlier state is still a new note. A poster without an
	// occurrence of its own asks for a fresh post ID instead.
	Occurrence int `json:"occurrence,omitempty"`
	// Detail is the evidence the agent reads: the instruction lines, the root
	// handoff, the clipped task of a spawn, or the transfer note.
	Detail string `json:"detail,omitempty"`
}

func (SessionEvent) eventFamily() EventFamily { return FamilySession }
func (s SessionEvent) eventType() EventType   { return EventType(s.Type) }

// eventSubject keeps every note of one session in that session's rows.
func (s SessionEvent) eventSubject() string { return s.Session }

// eventDedupe keys a note by the facts of its change, so a retried note posts
// once and a second change posts again.
func (s SessionEvent) eventDedupe() string {
	switch s.Type {
	case SessionRenamed:
		return s.OldName + "\x00" + s.Name
	case SessionPromoted, SessionAdopted:
		return s.From + "\x00" + s.To
	case SessionAccount:
		return s.From + "\x00" + s.To + "\x00" + string(s.Reason)
	case SessionInstructions:
		// The occurrence comes first, so a change that reverts to an earlier
		// state is still a new note while a retry of one change keeps its post.
		// The sections and the body follow it, because two different changes
		// to one section are two notes.
		lines := make([]string, len(s.Sections)+1)
		lines[0] = strconv.Itoa(s.Occurrence)
		for i, change := range s.Sections {
			lines[i+1] = string(change.Action) + " " + change.Name + "\x00" + strings.Join(change.Added, "\x01") + "\x00" + strings.Join(change.Removed, "\x01") + "\x02" + change.Body
		}
		return strings.Join(lines, "\x00")
	case SessionSeat:
		return s.Seat
	case SessionTransfer:
		return s.Transfer
	}
	return s.Profile
}

// InstructionBatchEvent summarizes one shared instruction change for the
// owner. The per-session SessionEvent remains the agent's own input in its DM;
// this quiet room event replaces the burst of owner-facing session rows.
type InstructionBatchEvent struct {
	Sections []InstructionChange `json:"sections"`
	Sessions []string            `json:"sessions"`
	// Notes names the per-session posts this summary replaces in the owner
	// feed. Root context skips those source posts and reads this row once.
	Notes []string `json:"notes"`
}

func (InstructionBatchEvent) eventFamily() EventFamily { return FamilySession }
func (InstructionBatchEvent) eventType() EventType     { return TypeInstructionBatch }
func (b InstructionBatchEvent) eventSubject() string   { return strings.Join(b.Sessions, "\x00") }

// eventDedupe is a fallback for callers without a source-note ID. The manager
// supplies an ID derived from the source notes, so a later identical edit is a
// new batch while a retry keeps its post.
func (b InstructionBatchEvent) eventDedupe() string {
	lines := make([]string, len(b.Sections))
	for i, change := range b.Sections {
		lines[i] = string(change.Action) + " " + change.Name + "\x00" + strings.Join(change.Added, "\x01") + "\x00" + strings.Join(change.Removed, "\x01") + "\x02" + change.Body
	}
	return strings.Join(lines, "\x00")
}

// AlertSource is what observed the condition an alert reports.
type AlertSource string

// Alert sources.
const (
	// AlertDoctor is a doctor report.
	AlertDoctor AlertSource = "doctor"
	// AlertEngine is the engine itself, as when it stays read-only.
	AlertEngine AlertSource = "engine"
	// AlertConsolidation is a session consolidation that paused with an error.
	AlertConsolidation AlertSource = "consolidation"
)

// alertSources lists every alert source the web needs as a union type.
var alertSources = []AlertSource{AlertDoctor, AlertEngine, AlertConsolidation}

// AlertSeverity is how urgent one finding of an alert is. A doctor ranks its
// causes, so its first finding is the one to act on now.
type AlertSeverity string

// Alert severities.
const (
	// AlertWarning is a cause the owner should fix soon.
	AlertWarning AlertSeverity = "warning"
	// AlertCritical is the cause that needs the owner now.
	AlertCritical AlertSeverity = "critical"
)

// alertSeverities lists every severity the web needs as a union type.
var alertSeverities = []AlertSeverity{AlertWarning, AlertCritical}

// AlertFinding is one cause an alert names, with the numbers behind it.
type AlertFinding struct {
	Severity AlertSeverity `json:"severity"`
	// Title states the cause in a few words, such as "machine is overloaded".
	Title string `json:"title"`
	// Cause is the evidence the title rests on, with its numbers.
	Cause string `json:"cause,omitempty"`
}

// AlertEvent tells the root seat that something needs the owner now: a doctor
// report, or the engine stuck read-only.
type AlertEvent struct {
	Source AlertSource `json:"source"`
	// ReportID names the doctor report in the machine's report list.
	ReportID string `json:"reportId,omitempty"`
	// Trigger names what started the check, such as "hook main-updated".
	Trigger string `json:"trigger,omitempty"`
	// Summary is the one line the alert states.
	Summary string `json:"summary,omitempty"`
	// Findings are the causes the doctor ranked, worst first.
	Findings []AlertFinding `json:"findings,omitempty"`
	// NextCommand is the one command the owner can run about the first doctor
	// finding, as a chip the card copies.
	NextCommand string `json:"nextCommand,omitempty"`
	// Remedy is the one command that fixes what an engine alert reports, as a
	// chip the card copies.
	Remedy string `json:"remedy,omitempty"`
	// Detail is the evidence the agent reads: the report text or the
	// migration failure.
	Detail string `json:"detail,omitempty"`
}

func (AlertEvent) eventFamily() EventFamily { return FamilyAlert }

// eventType reads the source: one alert type per observer.
func (a AlertEvent) eventType() EventType { return EventType(a.Source) }

func (a AlertEvent) eventSubject() string {
	if a.Source == AlertDoctor {
		return a.ReportID
	}
	return string(a.Source)
}

// eventDedupe keys one alert per report, and one per failure otherwise.
func (a AlertEvent) eventDedupe() string {
	if a.Source == AlertDoctor {
		return a.ReportID
	}
	return a.Detail
}

// ServiceRole is the kind of built-in service that failed.
type ServiceRole string

// Service roles.
const (
	ServiceLibrarian ServiceRole = "librarian"
	ServiceReviewer  ServiceRole = "reviewer"
	ServiceHelper    ServiceRole = "helper"
)

// serviceRoles lists every service role the web needs as a union type.
var serviceRoles = []ServiceRole{ServiceLibrarian, ServiceReviewer, ServiceHelper}

// ServiceFailedEvent tells the root seat that a service's turn failed. The
// service posts it once per turn, so a failing loop cannot flood the feed.
type ServiceFailedEvent struct {
	Service ServiceRole `json:"service"`
	Session string      `json:"session"`
	Turn    int         `json:"turn,omitempty"`
	Error   string      `json:"error"`
	Profile string      `json:"profile,omitempty"`
	Model   string      `json:"model,omitempty"`
	// WorkerStderr is the failed worker's standard-error tail, diagnostic
	// evidence the card discloses on its own, never the cause (D-108).
	WorkerStderr string `json:"workerStderr,omitempty"`
	// Detail is the evidence the agent reads: the failure report with the
	// session and how to inspect the service. The card shows Error.
	Detail string `json:"detail,omitempty"`
}

func (ServiceFailedEvent) eventFamily() EventFamily { return FamilyService }
func (ServiceFailedEvent) eventType() EventType     { return TypeFailed }

func (s ServiceFailedEvent) eventSubject() string { return s.Session }

// eventDedupe keys one alert per service turn.
func (s ServiceFailedEvent) eventDedupe() string { return strconv.Itoa(s.Turn) }

// TypeFailed is a failed service turn. TypeHelperOutcome is an automatic
// warning about a helper turn that finished without a deliverable result.
const (
	TypeFailed        EventType = "failed"
	TypeHelperOutcome EventType = "helper-outcome"
)

// HelperOutcomeReason is why a finished helper turn needs attention.
type HelperOutcomeReason string

// Helper outcome reasons.
const (
	HelperParentUnavailable HelperOutcomeReason = "parent-unavailable"
	HelperNoArtifact        HelperOutcomeReason = "no-artifact"
)

// helperOutcomeReasons lists every reason the web needs as a union type.
var helperOutcomeReasons = []HelperOutcomeReason{HelperParentUnavailable, HelperNoArtifact}

// String renders the reason in the event's agent-facing sentence.
func (r HelperOutcomeReason) String() string {
	switch r {
	case HelperParentUnavailable:
		return "its parent is unavailable"
	case HelperNoArtifact:
		return "it produced no artifact or findings for its parent"
	default:
		return string(r)
	}
}

// HelperOutcomeEvent tells a helper's parent or the owner that a helper turn
// finished without a deliverable result. The helper is event data; the engine
// posts the event, so the warning never borrows the helper's sender.
type HelperOutcomeEvent struct {
	Reason  HelperOutcomeReason `json:"reason"`
	Session string              `json:"session"`
	Turn    int                 `json:"turn"`
	// Detail keeps the inspection command with the agent-facing event text.
	Detail string `json:"detail,omitempty"`
}

func (HelperOutcomeEvent) eventFamily() EventFamily { return FamilyService }
func (HelperOutcomeEvent) eventType() EventType     { return TypeHelperOutcome }
func (h HelperOutcomeEvent) eventSubject() string   { return h.Session }

// eventDedupe keys one warning per helper turn and reason.
func (h HelperOutcomeEvent) eventDedupe() string {
	return strconv.Itoa(h.Turn) + "\x00" + string(h.Reason)
}

// EngineEvent is a note from the engine itself. The timed flush uses it to
// deliver the quiet posts a room held for its root seat.
type EngineEvent struct {
	// Minutes is how long the room held quiet posts for the root seat.
	Minutes int `json:"minutes"`
	// Detail is the cause, when the engine has one to add.
	Detail string `json:"detail,omitempty"`
}

func (EngineEvent) eventFamily() EventFamily { return FamilyEngine }
func (EngineEvent) eventType() EventType     { return TypeRootFlush }

// eventSubject keeps the flush out of any other engine note's rows.
func (EngineEvent) eventSubject() string { return "flush" }

func (e EngineEvent) eventDedupe() string { return strconv.Itoa(e.Minutes) }

// QuestionEvent is a note about a question. A withdrawn question keeps its
// place on the question's own card instead of arriving as a message from the
// session that withdrew it.
type QuestionEvent struct {
	// QuestionID is the question's short ID, such as q1.
	QuestionID string `json:"questionId"`
	// Post is the question's own post, which the note folds into.
	Post string `json:"post"`
	// By is who withdrew the question: the owner, root, or a session address.
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Detail is the note the agent read.
	Detail string `json:"detail,omitempty"`
}

func (QuestionEvent) eventFamily() EventFamily { return FamilyQuestion }
func (QuestionEvent) eventType() EventType     { return TypeWithdrawn }
func (q QuestionEvent) eventSubject() string   { return q.Post }
func (q QuestionEvent) eventDedupe() string    { return q.QuestionID }

// ReviewKind is the typed result of a reviewer turn.
type ReviewKind string

// Review kinds.
const (
	ReviewApprove     ReviewKind = "approve"
	ReviewChanges     ReviewKind = "changes"
	ReviewApproveFast ReviewKind = "approve-fast"
	ReviewStopEarly   ReviewKind = "stop-early"
)

// reviewKinds lists every review kind the web needs as a union type.
var reviewKinds = []ReviewKind{ReviewApprove, ReviewChanges, ReviewApproveFast, ReviewStopEarly}

// ReviewSeverity says how strongly a finding asks for a change.
type ReviewSeverity string

// Review severities.
const (
	ReviewBlocking  ReviewSeverity = "blocking"
	ReviewShouldFix ReviewSeverity = "should-fix"
	ReviewNit       ReviewSeverity = "nit"
)

// ReviewFinding is one typed finding of a changes review. It is the fact the
// card lists, not the Markdown the reviewer wrote.
type ReviewFinding struct {
	ID       string         `json:"id"`
	Severity ReviewSeverity `json:"severity"`
	File     string         `json:"file"`
	Line     int            `json:"line"`
	Why      string         `json:"why"`
	Fix      string         `json:"fix"`
	Patch    string         `json:"patch,omitempty"`
}

// ReviewStopReason is why a reviewer stopped before it judged the change.
type ReviewStopReason string

// Review stop reasons.
const (
	ReviewSuperseded ReviewStopReason = "superseded"
	ReviewDuplicate  ReviewStopReason = "duplicate"
	ReviewEmpty      ReviewStopReason = "empty"
	ReviewTooLarge   ReviewStopReason = "too-large"
)

// ReviewEvent tells a room how one reviewer turn ended: the verdict, the
// findings by severity, or a stop reason. It is folded
// with the events of one PR head, so a re-review of a new head is a new card.
type ReviewEvent struct {
	Kind     ReviewKind `json:"kind"`
	ChangeID string     `json:"changeId"`
	Number   int        `json:"number,omitempty"`
	Label    string     `json:"label"`
	Location string     `json:"location"`
	Head     string     `json:"head"`
	// Summary is the reviewer's one-line facts line, when it has one.
	Summary string `json:"summary,omitempty"`
	// Findings lists a changes review's findings, newest order kept.
	Findings []ReviewFinding `json:"findings,omitempty"`
	// Blocking, ShouldFix and Nits count the findings by severity, so the chip
	// never recounts a list the card may not show.
	Blocking  int `json:"blocking,omitempty"`
	ShouldFix int `json:"shouldFix,omitempty"`
	Nits      int `json:"nits,omitempty"`
	// StopReason is set only by a stop-early result.
	StopReason ReviewStopReason `json:"stopReason,omitempty"`
	// FastLand reports an approve-fast that uses direct publication. Keeping
	// this Boolean also preserves the meaning of older review events.
	FastLand bool `json:"fastLand,omitempty"`
	// Detail is the reviewer's Markdown body, which the agent text appends and
	// no card parses.
	Detail string `json:"detail,omitempty"`
}

func (ReviewEvent) eventFamily() EventFamily { return FamilyReview }
func (ReviewEvent) eventType() EventType     { return TypeResult }

// eventSubject keeps the events of one PR head together and apart from another
// head's result.
func (e ReviewEvent) eventSubject() string { return e.ChangeID + "\x00" + e.Head }

// eventDedupe keys one result per kind and head: a retried verdict keeps its
// post, and a different result on the same head posts again.
func (e ReviewEvent) eventDedupe() string { return string(e.Kind) }

// ShellState is how a detached shell job ended: with its command's exit
// status, or stopped before it finished.
type ShellState string

// Shell states.
const (
	ShellCompleted ShellState = "completed"
	ShellFailed    ShellState = "failed"
	ShellStopped   ShellState = "stopped"
)

// shellStates lists every shell state the web needs as a union type.
var shellStates = []ShellState{ShellCompleted, ShellFailed, ShellStopped}

// ShellEvent is the result of a detached `dyna shell` job: the facts its row
// shows, so the row never reads the post text. The job ID stays opaque: it is
// the row's own key and the way to the stored output, never a label.
type ShellEvent struct {
	// ShellID is the shell store's job ID.
	ShellID string `json:"shellId"`
	// Label is the human label of the command: its first word, and the short
	// description its caller gave.
	Label string     `json:"label"`
	State ShellState `json:"state"`
	// ExitCode is the command's exit status, -1 when a stop ended the job
	// before it finished.
	ExitCode   int `json:"exitCode,omitempty"`
	DurationMS int `json:"durationMs,omitempty"`
	// Output is the last lines of the job's output. The row shows them behind
	// its own toggle only, never in the room feed it is hidden from.
	Output []string `json:"output,omitempty"`
	// Parent is the session that started the job, empty when the owner did.
	Parent string `json:"parent,omitempty"`
	// Detail is the rest of the line an agent reads, when the state and the
	// exit code do not say it: where the full output lives.
	Detail string `json:"detail,omitempty"`
}

func (ShellEvent) eventFamily() EventFamily { return FamilyShell }
func (ShellEvent) eventType() EventType     { return TypeResult }
func (s ShellEvent) eventSubject() string   { return s.ShellID }

// eventDedupe keys one result per job, so a second notice of the same job
// posts nothing.
func (s ShellEvent) eventDedupe() string { return s.ShellID }

// RunState is how a detached workflow run ended.
type RunState string

// Run states.
const (
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
	RunStopped   RunState = "stopped"
)

// runStates lists every run state the web needs as a union type.
var runStates = []RunState{RunCompleted, RunFailed, RunStopped}

// RunEvent is the result of a detached workflow run: the facts its card
// shows, so the card never reads the post text.
type RunEvent struct {
	// RunID is the workflow's run ID: the card's key and the way to its view.
	RunID string `json:"runId"`
	// Name is the workflow's name, as its script wrote it.
	Name  string   `json:"name"`
	State RunState `json:"state"`
	// Summary is the run's result as one short line, or the failure cause.
	Summary string `json:"summary,omitempty"`
	// Steps is how many agent calls the run recorded, and Agents how many
	// worker sessions served them.
	Steps      int `json:"steps,omitempty"`
	Agents     int `json:"agents,omitempty"`
	DurationMS int `json:"durationMs,omitempty"`
	// Parent is the session that started the run, empty when an external
	// orchestrator or the owner did.
	Parent string `json:"parent,omitempty"`
	// Detail is the rest of the line an agent reads: where the full output
	// lives, or the error the summary cuts short.
	Detail string `json:"detail,omitempty"`
}

func (RunEvent) eventFamily() EventFamily { return FamilyRun }
func (RunEvent) eventType() EventType     { return TypeResult }
func (r RunEvent) eventSubject() string   { return r.RunID }
func (r RunEvent) eventDedupe() string    { return r.RunID }

// TurnState is how a session turn ended.
type TurnState string

// Turn states.
const (
	TurnCompleted TurnState = "completed"
	TurnFailed    TurnState = "failed"
	TurnStopped   TurnState = "stopped"
)

// turnStates lists every turn state the web needs as a union type.
var turnStates = []TurnState{TurnCompleted, TurnFailed, TurnStopped}

// TurnEvent is the end of one session turn. Its summary is the report the
// session gave, which the completion row shows as its body.
type TurnEvent struct {
	// Session is the address of the session that ended the turn, as the post
	// reports it, and SessionID its exact session.
	Session   string    `json:"session"`
	SessionID string    `json:"sessionId"`
	Turn      int       `json:"turn"`
	State     TurnState `json:"state"`
	// Summary is the report the session gave, as markdown. A failed turn has
	// the error instead.
	Summary string `json:"summary,omitempty"`
	// Error is why the turn failed, when it did.
	Error string `json:"error,omitempty"`
	// WorkerStderr is the failed worker's standard-error tail, diagnostic
	// evidence the card discloses on its own, never the cause (D-108).
	WorkerStderr string `json:"workerStderr,omitempty"`
	// Detail is the rest of the line an agent reads: how to inspect the turn
	// or recover from it.
	Detail string `json:"detail,omitempty"`
}

func (TurnEvent) eventFamily() EventFamily { return FamilyTurn }
func (t TurnEvent) eventSubject() string   { return t.Session }

// eventType reads the state: each end of a turn has its own type, so a card
// and a feed view pick their tone from the registry.
func (t TurnEvent) eventType() EventType {
	switch t.State {
	case TurnFailed:
		return TypeTurnFailed
	case TurnStopped:
		return TypeTurnStopped
	}
	return TypeTurnCompleted
}

// eventDedupe keys one post per session and turn, so a retried report keeps
// its post.
func (t TurnEvent) eventDedupe() string { return strconv.Itoa(t.Turn) + "\x00" + string(t.State) }

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
	// NoWatch keeps the event out of `dyna room watch` and its text log, even
	// though agents receive it: a shell or run result lives in its own view.
	NoWatch bool
	Wake    WakeRule
	// RootOnOwnerReport starts the root seat when the post is owed to the
	// owner (Message.OwnerReport). A shell or run the root started wakes it on
	// its result; a completed turn does not, and only a failure or a stop does.
	RootOnOwnerReport bool
	// Once drops a later post with the same key instead of appending it.
	Once bool
	Text string
	// Blocks are the payload fields that the agent text appends after the
	// template, in order. Empty means the default blocks: detail and logTail.
	Blocks []string
	// TextNoSubject is the summary for an event that names no subject, such
	// as a cleanup failure that has no single tree.
	TextNoSubject string
	// Audience is who the post belongs to. Empty means the room's own
	// conversation.
	Audience Audience
	// Producers lists the services that may post this event, and the producer
	// it names. Empty means the engine itself posts it.
	Producers []EventProducer
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
		Audience: AudienceSession,
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
	// Issue events are a backlog: they never wake anyone, root included. The
	// owner feed keeps them, and the assignee reads a quiet notice there.
	{
		Family:   FamilyIssue,
		Type:     EventType(IssueFiled),
		Renderer: "IssueCard",
		Payload:  reflect.TypeOf(IssueEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{filer} filed issue D-{number}: {title}",
	},
	{
		Family:   FamilyIssue,
		Type:     EventType(IssueUpdated),
		Renderer: "IssueCard",
		Payload:  reflect.TypeOf(IssueEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{actor} changed issue D-{number} to {state}: {title}",
	},
	{
		Family:   FamilyIssue,
		Type:     EventType(IssueClosed),
		Renderer: "IssueCard",
		Payload:  reflect.TypeOf(IssueEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{actor} closed issue D-{number}: {reason}",
	},
	// A goal change reaches the owner feed only, so it wakes nobody.
	{
		Family:   FamilyGoal,
		Type:     EventType(GoalAdded),
		Renderer: "GoalPost",
		Payload:  reflect.TypeOf(GoalEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{actor} added goal {goalId}: {title}",
	},
	{
		Family:   FamilyGoal,
		Type:     EventType(GoalEdited),
		Renderer: "GoalPost",
		Payload:  reflect.TypeOf(GoalEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{actor} edited goal {goalId}: {title}",
	},
	{
		Family:   FamilyGoal,
		Type:     EventType(GoalAmended),
		Renderer: "GoalPost",
		Payload:  reflect.TypeOf(GoalEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Text:     "{actor} amended the brief of goal {goalId}: {summary}",
	},
	{
		Family:   FamilyGoal,
		Type:     EventType(GoalItemsDone),
		Renderer: "GoalPost",
		Payload:  reflect.TypeOf(GoalEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Text:     "{actor} closed the last item of goal {goalId}: {title}. The goal stays open until its holder finishes it.",
	},
	{
		Family:   FamilyGoal,
		Type:     EventType(GoalFinished),
		Renderer: "GoalPost",
		Payload:  reflect.TypeOf(GoalEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Goal {goalId} {title} is done: {summary}",
	},
	{
		Family:   FamilyGoal,
		Type:     EventType(GoalDropped),
		Renderer: "GoalPost",
		Payload:  reflect.TypeOf(GoalEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{actor} dropped goal {goalId}: {title}",
	},
	// A check-in starts the parent, as the free-text check-in always did, and
	// reaches no other session.
	{
		Family:    FamilyCheckin,
		Type:      TypeSummary,
		Renderer:  "CheckinCard",
		Payload:   reflect.TypeOf(CheckinEvent{}),
		Wake:      WakeTarget,
		Once:      true,
		Text:      checkinText,
		Audience:  AudienceSession,
		Producers: []EventProducer{ProducerParentCheckin},
	},
	// A slow test is a quiet CI health line in the owner feed.
	{
		Family:   FamilyCIHealth,
		Type:     TypeSlowTest,
		Renderer: "SlowTestCard",
		Payload:  reflect.TypeOf(SlowTestEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{count} slow tests crossed the CI limit in run {run}.",
	},
	// A rule change is a quiet note in the owner feed.
	{
		Family:   FamilyRule,
		Type:     TypeChanged,
		Renderer: "RuleCard",
		Payload:  reflect.TypeOf(RuleEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{actor} {verb} the {scope} rule {ruleId}",
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
	// A merge-fix request is the owner's or root's recorded answer to a red
	// main branch: one PR lands, and the room keeps the reason as a card. Only
	// the owner's request needs root, which reads why main CI was bypassed.
	{
		Family:   FamilyMerge,
		Type:     EventType(MergeFixRequested),
		Renderer: "MergeCard",
		Payload:  reflect.TypeOf(MergeEvent{}),
		Wake:     WakeRootIfNeeded,
		Once:     true,
		Text:     "Merge fix requested by {actor} for {label} at {head}. This one merge bypasses main CI; fresh PR checks and approval remain required.",
	},
	// A machine that is low on disk warns root once per low-disk period, and
	// holds its CI work until space is freed.
	{
		Family:   FamilyMachine,
		Type:     TypeLowDisk,
		Renderer: "MachineDiskCard",
		Payload:  reflect.TypeOf(MachineEvent{}),
		Wake:     WakeRootIfNeeded,
		Once:     true,
		Text:     "Machine {name} is low on disk: {summary}. Dyna starts no CI there until space is freed.",
	},
	// A run that repaired a linked cache posts one quiet machine fact, so the
	// owner sees that Dyna switched it back without waking a session.
	{
		Family:   FamilyMachine,
		Type:     TypeCacheRepaired,
		Renderer: "MachineCacheCard",
		Payload:  reflect.TypeOf(MachineCacheEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Machine {name} switched linked CI caches back to managed directories: {summary}. The old links are kept beside them as backups.",
	},
	// The owner's answer to a call request is a fact of the room: root asked for
	// the call and stops waiting when it reads the answer.
	{
		Family:   FamilyCall,
		Type:     EventType(CallJoined),
		Renderer: "CallCard",
		Payload:  reflect.TypeOf(CallEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Text:     "The owner joined the call {call}.",
	},
	{
		Family:   FamilyCall,
		Type:     EventType(CallDeclined),
		Renderer: "CallCard",
		Payload:  reflect.TypeOf(CallEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Text:     "The owner declined the call {call}.",
	},
	{
		Family:   FamilyCall,
		Type:     EventType(CallWithdrawn),
		Renderer: "CallCard",
		Payload:  reflect.TypeOf(CallEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Text:     "The call request {call} was withdrawn.",
	},
	// A review result wakes the PR author only when it asks for changes. The
	// watcher posts an approval or a stop-early quietly to the room feed.
	{
		Family:   FamilyReview,
		Type:     TypeResult,
		Renderer: "ReviewResultCard",
		Payload:  reflect.TypeOf(ReviewEvent{}),
		Wake:     WakeTarget,
		Text:     "{location}\nHead: {head}\n\nReviewer result for {label}: {kind}.",
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
	// A session note tells a parent or the session itself what changed about
	// it. Every note is quiet: no note asks anyone to act now.
	{
		Family:   FamilySession,
		Type:     EventType(SessionRenamed),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Audience: AudienceSession,
		Once:     true,
		Text:     "Session {oldName} is now {name}.",
	},
	{
		Family:   FamilySession,
		Type:     EventType(SessionPromoted),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Audience: AudienceSession,
		Once:     true,
		Text:     "{session} moved from {from} to {to}.",
	},
	{
		Family:   FamilySession,
		Type:     EventType(SessionAdopted),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Audience: AudienceSession,
		Once:     true,
		Text:     "{session} moved from {from} to {to}.",
	},
	{
		Family:   FamilySession,
		Type:     EventType(SessionAccount),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{session}: {from} moved to {to} ({reason}).",
	},
	{
		Family:   FamilySession,
		Type:     EventType(SessionInstructions),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		// The session applies this note from its next turn, so it is agent
		// input. It wakes nobody: the running turn reads it as it arrives.
		Wake:     WakeNever,
		Audience: AudienceSession,
		Once:     true,
		Text:     "[DYNA INSTRUCTIONS CHANGED] Since this conversation started, your standing instructions changed. These changes apply now:",
	},
	// A shared change has one quiet owner row. The per-session notes above
	// remain agent input in each session's DM.
	{
		Family:   FamilySession,
		Type:     TypeInstructionBatch,
		Renderer: "InstructionBatchCard",
		Payload:  reflect.TypeOf(InstructionBatchEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Audience: AudienceRoom,
		Once:     true,
		Text:     "Standing instructions changed for {sessions}.",
	},
	{
		Family:   FamilySession,
		Type:     EventType(SessionSeat),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		// A seat note is root input whether or not it carries a handoff, and it
		// wakes root only when one asks for action. A seat can change more than
		// once with the same label, so each note is its own post.
		Wake: WakeRootIfNeeded,
		Text: "{seat}",
	},
	{
		Family:   FamilySession,
		Type:     EventType(SessionSpawned),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "Started @{name} ({profile}) in {dir}:",
	},
	// A model transfer asks the running session for its handoff. The session
	// must answer, so the ask is agent input: it wakes the session it is
	// addressed to, and its own DM keeps the instructions behind Details.
	{
		Family:   FamilySession,
		Type:     EventType(SessionTransfer),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Wake:     WakeTarget,
		Audience: AudienceSession,
		Once:     true,
		Text:     "[DYNA MODEL TRANSFER] Prepare a handoff for transfer {transfer}:",
	},
	// An alert needs the root seat now, so it always wakes it. The doctor and
	// the engine are the only posters.
	{
		Family:    FamilyAlert,
		Type:      EventType(AlertDoctor),
		Renderer:  "AlertCard",
		Payload:   reflect.TypeOf(AlertEvent{}),
		Wake:      WakeRoot,
		Once:      true,
		Text:      "Doctor report {reportId} ({trigger}):",
		Producers: []EventProducer{ProducerDoctor},
	},
	{
		Family:    FamilyAlert,
		Type:      EventType(AlertEngine),
		Renderer:  "AlertCard",
		Payload:   reflect.TypeOf(AlertEvent{}),
		Wake:      WakeRoot,
		Once:      true,
		Text:      "The engine has been read-only for more than 2 minutes because migration failed:",
		Producers: []EventProducer{ProducerEngine},
	},
	{
		Family:    FamilyAlert,
		Type:      EventType(AlertConsolidation),
		Renderer:  "AlertCard",
		Payload:   reflect.TypeOf(AlertEvent{}),
		Wake:      WakeRoot,
		Once:      true,
		Text:      "A session consolidation paused with an error:",
		Producers: []EventProducer{ProducerEngine},
	},
	// A failed service turn needs the root seat, once per turn.
	{
		Family:    FamilyService,
		Type:      TypeFailed,
		Renderer:  "ServiceAlertCard",
		Payload:   reflect.TypeOf(ServiceFailedEvent{}),
		Wake:      WakeRoot,
		Once:      true,
		Text:      "The {service} service failed on turn {turn} ({session}): {error}",
		Producers: []EventProducer{ProducerLibrarian, ProducerReviewer, ProducerHelper},
	},
	// A helper outcome wakes its available parent. An unavailable parent sends
	// the warning to the owner with NeedsRoot, which wakes the root seat too.
	{
		Family:    FamilyService,
		Type:      TypeHelperOutcome,
		Renderer:  "HelperOutcomeRow",
		Payload:   reflect.TypeOf(HelperOutcomeEvent{}),
		Wake:      WakeTarget,
		Once:      true,
		Text:      "Helper {session} turn {turn} finished, but {reason}.",
		Producers: []EventProducer{ProducerHelper},
	},
	// The timed flush is the engine asking the root seat to read the held
	// batch, so it wakes root and nothing else.
	{
		Family:   FamilyEngine,
		Type:     TypeRootFlush,
		Renderer: "EngineNoticeCard",
		Payload:  reflect.TypeOf(EngineEvent{}),
		Wake:     WakeRoot,
		Text:     "[TIMED FLUSH] No post woke the root seat for {minutes} minutes, so this notice delivers the held quiet posts as one batch. They did not ask for action; act only where one of them needs a root decision. The full posts stay in the room log.",
	},
	// A withdrawn question is a quiet note on the question's own card.
	{
		Family:   FamilyQuestion,
		Type:     TypeWithdrawn,
		Renderer: "QuestionNoteCard",
		Payload:  reflect.TypeOf(QuestionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Question {questionId} was withdrawn: {reason}",
	},
	// A shell result is the parent's own command, so it wakes the parent and
	// reaches the owner feed as a row. It stays out of the room watch and out
	// of the room chat: the session DM and the shell view carry it.
	{
		Family:            FamilyShell,
		Type:              TypeResult,
		Renderer:          "ShellResultRow",
		Payload:           reflect.TypeOf(ShellEvent{}),
		NoWatch:           true,
		Wake:              WakeTarget,
		Once:              true,
		RootOnOwnerReport: true,
		Audience:          AudienceSession,
		Blocks:            []string{"output", "detail"},
		Text:              "Shell job {label} {state} with exit code {exitCode}.",
		Producers:         []EventProducer{ProducerShell},
	},
	// A workflow run result is the parent's work too, and stays out of the
	// room watch for the same reason as a shell result.
	{
		Family:            FamilyRun,
		Type:              TypeResult,
		Renderer:          "RunResultCard",
		Payload:           reflect.TypeOf(RunEvent{}),
		NoWatch:           true,
		Wake:              WakeTarget,
		Once:              true,
		RootOnOwnerReport: true,
		Blocks:            []string{"summary", "detail"},
		Text:              "Workflow run {name} {state}.",
		Producers:         []EventProducer{ProducerWorkflow},
	},
	// The end of a turn is a row in the feed and a card in a session DM. A
	// completed turn is quiet for root unless root asked for it; a failure and
	// a stop wake root when they are owed to it (RootOnOwnerReport).
	{
		Family:   FamilyTurn,
		Type:     TypeTurnCompleted,
		Renderer: "TurnCompletionRow",
		Payload:  reflect.TypeOf(TurnEvent{}),
		Wake:     WakeTarget,
		Blocks:   []string{"summary", "detail"},
		Text:     "{session} turn {turn} completed.",
	},
	{
		Family:            FamilyTurn,
		Type:              TypeTurnFailed,
		Renderer:          "TurnCompletionRow",
		Payload:           reflect.TypeOf(TurnEvent{}),
		Wake:              WakeTarget,
		RootOnOwnerReport: true,
		Blocks:            []string{"error", "summary", "detail"},
		Text:              "{session} turn {turn} failed.",
	},
	{
		Family:            FamilyTurn,
		Type:              TypeTurnStopped,
		Renderer:          "TurnCompletionRow",
		Payload:           reflect.TypeOf(TurnEvent{}),
		Wake:              WakeTarget,
		RootOnOwnerReport: true,
		Blocks:            []string{"summary", "detail"},
		Text:              "{session} turn {turn} stopped.",
	},
}

// eventEnum is a named string type whose values the web needs as a union.
type eventEnum struct {
	Type   reflect.Type
	Values []string
}

// checkinText is the instruction a parent receives. The child table follows
// it as the event's detail block.
const checkinText = "Check on your children. For each one below, confirm it is making progress. If one is stuck, idle with unfinished work, or waiting for something that already happened, nudge it, restart it, or take over its work. Close children whose work is done. If everything is on track, end your turn without posting."

var eventEnums = []eventEnum{
	{Type: reflect.TypeOf(PRType("")), Values: enumValues(prTypes)},
	{Type: reflect.TypeOf(MergeStep("")), Values: enumValues(mergeSteps)},
	{Type: reflect.TypeOf(IssueChange("")), Values: enumValues(issueChanges)},
	{Type: reflect.TypeOf(IssueState("")), Values: enumValues(issueStates)},
	{Type: reflect.TypeOf(GoalChange("")), Values: enumValues(goalChanges)},
	{Type: reflect.TypeOf(GoalState("")), Values: enumValues(goalStates)},
	{Type: reflect.TypeOf(CheckinState("")), Values: enumValues(checkinStates)},
	{Type: reflect.TypeOf(SessionChange("")), Values: enumValues(sessionChanges)},
	{Type: reflect.TypeOf(AccountReason("")), Values: enumValues(accountReasons)},
	{Type: reflect.TypeOf(InstructionAction("")), Values: enumValues(instructionActions)},
	{Type: reflect.TypeOf(AlertSource("")), Values: enumValues(alertSources)},
	{Type: reflect.TypeOf(AlertSeverity("")), Values: enumValues(alertSeverities)},
	{Type: reflect.TypeOf(ServiceRole("")), Values: enumValues(serviceRoles)},
	{Type: reflect.TypeOf(HelperOutcomeReason("")), Values: enumValues(helperOutcomeReasons)},
	{Type: reflect.TypeOf(ShellState("")), Values: enumValues(shellStates)},
	{Type: reflect.TypeOf(RunState("")), Values: enumValues(runStates)},
	{Type: reflect.TypeOf(TurnState("")), Values: enumValues(turnStates)},
	{Type: reflect.TypeOf(RuleVerb("")), Values: enumValues([]RuleVerb{RuleAdded, RuleEdited, RuleRemoved})},
	{Type: reflect.TypeOf(WorktreeReason("")), Values: []string{
		string(WorktreeNotMerged), string(WorktreeDirty), string(WorktreeManualHandoff), string(WorktreeKeepFailed), string(WorktreeOther),
	}},
	{Type: reflect.TypeOf(HookState("")), Values: []string{
		string(HookRunning), string(HookPassed), string(HookFailed), string(HookError),
	}},
	{Type: reflect.TypeOf(MergeReasonKind("")), Values: enumValues(mergeReasonKinds)},
	{Type: reflect.TypeOf(ReviewKind("")), Values: enumValues(reviewKinds)},
	{Type: reflect.TypeOf(ReviewSeverity("")), Values: []string{
		string(ReviewBlocking), string(ReviewShouldFix), string(ReviewNit),
	}},
	{Type: reflect.TypeOf(ReviewStopReason("")), Values: []string{
		string(ReviewSuperseded), string(ReviewDuplicate), string(ReviewEmpty), string(ReviewTooLarge),
	}},
	{Type: reflect.TypeOf(CallState("")), Values: enumValues(callStates)},
}

// eventObjects are the struct types that an event payload embeds. The
// generator writes an interface and a guard for each, so a card reads a typed
// list instead of a stringly field.
var eventObjects = []reflect.Type{
	reflect.TypeOf(MergePR{}),
	reflect.TypeOf(MergeCheckSummary{}),
	reflect.TypeOf(InstructionChange{}),
	reflect.TypeOf(AlertFinding{}),
	reflect.TypeOf(ReviewFinding{}),
}

// eventOpenStrings are named string types whose values are open, such as a
// session address or the engine that made a change. The generator reads each
// as a plain string, because a union of its values would be incomplete.
var eventOpenStrings = []reflect.Type{reflect.TypeOf(IssueActor(""))}

// voiceCallKinds are the posts of the owner's voice call itself: the owner's
// spoken words, Voice's replies and Voice's requests to root. A person or the
// voice service says them aloud, so they are not system posts and no event
// registry entry describes them. Store.Post keeps accepting them.
//
// Every other system post goes through PostEvent, so this list holds no
// family with a registry entry and there is no legacy kind left.
var voiceCallKinds = []string{"voice"}

// agentKinds are the posts a person or a session writes. A service's own alert
// is a session post too: the service session writes it to its owner or parent.
// Every other system post goes through PostEvent, so the turn, shell and run
// results this PR types are no kinds here any more.
var agentKinds = []string{"message", "service-alert"}

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
	if ev.eventSubject() == "" && spec.TextNoSubject != "" {
		spec.Text = spec.TextNoSubject
	}
	return EventPost{Kind: string(spec.Family), Type: spec.Type, Text: eventText(spec, ev), Quiet: spec.Quiet, Wake: spec.Wake}, true
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

// voiceCallPost reports a kind that only the owner's voice call writes.
func voiceCallPost(kind string) bool { return slices.Contains(voiceCallKinds, kind) }

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
	// Producer names the service that observed the event. A spec that lists
	// producers accepts only one of them, and requires one.
	Producer EventProducer
	// SessionId binds participant addresses to exact sessions.
	SessionIDs map[string]string
	// SenderRoomID is the room of the sending session, from its stored
	// session. Empty for the engine and the room's own service.
	SenderRoomID string
	// Conversation names the conversation the post belongs to, when the poster
	// writes in a DM rather than in the room.
	Conversation string
	ReplyTo      string
	// Attachments are the files the poster stored for this post, such as the
	// full text of a truncated root handoff.
	Attachments []Attachment
	// OwnerReport marks the post as a report owed to the room owner, which
	// root reads in its own feed.
	OwnerReport bool
	// Turn is the turn number of a turn-end event, when the poster knows it.
	Turn int
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
	// A caller cannot choose the sender of an event post: it is the engine, or
	// the room's own service. A service that observes an event is the event's
	// producer, which is data on the post, never a sender.
	if len(spec.Producers) > 0 && !slices.Contains(spec.Producers, in.Producer) {
		return Message{}, fmt.Errorf("a %s/%s event is posted by %v, not %q", spec.Family, spec.Type, spec.Producers, in.Producer)
	}
	from := EngineSender
	if in.RoomService {
		rec, err := s.readRecord(in.RoomID)
		if err != nil {
			return Message{}, err
		}
		from = rec.Name
	}
	// An event that wakes the root seat is addressed to that seat and to no
	// one else, so a later card cannot claim root was woken by a post to a
	// session.
	if spec.Wake == WakeRoot {
		rec, err := s.readRecord(in.RoomID)
		if err != nil {
			return Message{}, err
		}
		switch len(in.To) {
		case 0:
			return Message{}, fmt.Errorf("a %s/%s event wakes the root seat, so it needs the root seat as its recipient", spec.Family, spec.Type)
		default:
			for _, addr := range in.To {
				if addr != rec.Name && addr != rec.Name+":"+RootAgent {
					return Message{}, fmt.Errorf("a %s/%s event wakes only the root seat, so it is addressed to %s or %s, not %q", spec.Family, spec.Type, rec.Name, rec.Name+":"+RootAgent, addr)
				}
			}
		}
	}
	template := spec.Text
	if ev.eventSubject() == "" && spec.TextNoSubject != "" {
		template = spec.TextNoSubject
	}
	text := eventText(EventSpec{Text: template, Blocks: spec.Blocks}, ev)
	if spec.Wake == WakeRootIfNeeded && !in.NeedsRoot {
		spec.Wake = WakeNever
	}
	id := in.ID
	if id == "" && !in.FreshID {
		id = eventID(ev)
	}
	msg, err := s.post(in.RoomID, PostInput{
		ID:           id,
		From:         from,
		SenderRoomID: in.SenderRoomID,
		Kind:         string(spec.Family),
		Notice:       EventNotice{Kind: string(spec.Type), Producer: in.Producer},
		payload:      EventPayload{raw: payload},
		To:           in.To,
		Via:          in.Via,
		NeedsRoot:    in.NeedsRoot,
		SessionIDs:   in.SessionIDs,
		Conversation: Conversation(in.Conversation),
		ReplyTo:      in.ReplyTo,
		Text:         text,
		Attachments:  in.Attachments,
		Turn:         in.Turn,
		OwnerReport:  in.OwnerReport,
		Wake:         spec.Wake != WakeNever && !in.NoWake,
	}, true)
	if errors.Is(err, ErrPostIDConflict) && spec.Once {
		return Message{}, nil
	}
	return msg, err
}

// enumValues lists the values of a named string type for the generated web
// union.
func enumValues[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
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
// paragraph after the spec's one-line template, in this order. A spec with no
// blocks of its own appends detail and logTail. A block that the event leaves
// empty adds nothing, so the text has no empty section.
var eventBlocks = []string{"detail", "logTail"}

// eventText renders the spec's one-line template and appends the event's text
// blocks. A block that only repeats the template adds nothing. A block that is
// a list of strings, such as the tail lines of a shell, keeps one line per
// item.
func eventText(spec EventSpec, ev Event) string {
	text := renderEventText(spec.Text, ev)
	blocks := spec.Blocks
	if len(blocks) == 0 {
		blocks = eventBlocks
	}
	value := reflect.ValueOf(ev)
	for _, name := range blocks {
		block := strings.TrimSpace(eventBlockText(value, name))
		if block == "" || block == strings.TrimSpace(text) {
			continue
		}
		text += "\n\n" + block
	}
	return text
}

// eventBlockText formats one block field of an event. A list field, such as the
// tail lines of a shell, is one line per item.
func eventBlockText(value reflect.Value, name string) string {
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName != name || value.Field(i).Kind() != reflect.Slice {
			continue
		}
		items := value.Field(i)
		if items.Len() == 0 {
			return ""
		}
		lines := make([]string, items.Len())
		for j := range lines {
			lines[j] = fmt.Sprint(items.Index(j).Interface())
		}
		return strings.Join(lines, "\n")
	}
	return eventFieldText(value, name)
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

// EventFromPayload rebuilds a stored event from its family, type and payload
// bytes, so a saved report plan replays exactly the event it prepared.
func EventFromPayload(family, typ string, payload []byte) (Event, error) {
	spec, ok := eventSpecFor(family, typ)
	if !ok {
		return nil, fmt.Errorf("event %s/%s is not registered in internal/room/events.go", family, typ)
	}
	ptr := reflect.New(spec.Payload)
	if err := decodeEventPayload(payload, ptr.Interface()); err != nil {
		return nil, err
	}
	// Elem gives the event value itself: an event is a value, and a pointer to
	// one would break the reflection that reads its fields.
	ev, ok := ptr.Elem().Interface().(Event)
	if !ok {
		return nil, fmt.Errorf("event %s/%s has no payload type", family, typ)
	}
	return ev, nil
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
			// A named string type may name itself in words, as IssueActor does
			// for the engine. A plain string names itself.
			if fieldValue.CanInterface() {
				if named, ok := fieldValue.Interface().(fmt.Stringer); ok {
					return named.String()
				}
			}
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

// Shell returns the payload of a shell result: the facts its row shows.
func (m Message) Shell() (ShellEvent, bool) {
	if m.Kind != string(FamilyShell) || m.Notice.Kind != string(TypeResult) || len(m.Payload.raw) == 0 {
		return ShellEvent{}, false
	}
	var ev ShellEvent
	if err := decodeEventPayload(m.Payload.raw, &ev); err != nil || ev.ShellID == "" {
		return ShellEvent{}, false
	}
	return ev, true
}

// RunResult returns the payload of a workflow run result.
func (m Message) RunResult() (RunEvent, bool) {
	if m.Kind != string(FamilyRun) || m.Notice.Kind != string(TypeResult) || len(m.Payload.raw) == 0 {
		return RunEvent{}, false
	}
	var ev RunEvent
	if err := decodeEventPayload(m.Payload.raw, &ev); err != nil || ev.RunID == "" {
		return RunEvent{}, false
	}
	return ev, true
}

// TurnResult returns the payload of the end of a session turn, of any of its
// three states.
func (m Message) TurnResult() (TurnEvent, bool) {
	if m.Kind != string(FamilyTurn) || len(m.Payload.raw) == 0 {
		return TurnEvent{}, false
	}
	switch m.Notice.Kind {
	case string(TypeTurnCompleted), string(TypeTurnFailed), string(TypeTurnStopped):
	default:
		return TurnEvent{}, false
	}
	var ev TurnEvent
	if err := decodeEventPayload(m.Payload.raw, &ev); err != nil || ev.Session == "" || ev.Turn < 1 {
		return TurnEvent{}, false
	}
	return ev, true
}

// decodeEventPayload decodes a stored event payload strictly, so an unknown
// field is a failed read rather than a silent mismatch.
func decodeEventPayload(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
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

// Issue returns the payload of an issue event.
func (m Message) Issue() (IssueEvent, bool) {
	if m.Kind != string(FamilyIssue) || len(m.Payload.raw) == 0 {
		return IssueEvent{}, false
	}
	var ev IssueEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Type == "" {
		return IssueEvent{}, false
	}
	return ev, true
}

// Goal returns the payload of a goal event.
func (m Message) Goal() (GoalEvent, bool) {
	if m.Kind != string(FamilyGoal) || len(m.Payload.raw) == 0 {
		return GoalEvent{}, false
	}
	var ev GoalEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Type == "" {
		return GoalEvent{}, false
	}
	return ev, true
}

// Checkin returns the payload of a parent check-in.
func (m Message) Checkin() (CheckinEvent, bool) {
	if m.Kind != string(FamilyCheckin) || m.Notice.Kind != string(TypeSummary) || len(m.Payload.raw) == 0 {
		return CheckinEvent{}, false
	}
	var ev CheckinEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return CheckinEvent{}, false
	}
	return ev, true
}

// SlowTest returns the payload of a CI health line about a slow test.
func (m Message) SlowTest() (SlowTestEvent, bool) {
	if m.Kind != string(FamilyCIHealth) || m.Notice.Kind != string(TypeSlowTest) || len(m.Payload.raw) == 0 {
		return SlowTestEvent{}, false
	}
	var ev SlowTestEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return SlowTestEvent{}, false
	}
	return ev, true
}

// Rule returns the payload of a rule change.
func (m Message) Rule() (RuleEvent, bool) {
	if m.Kind != string(FamilyRule) || m.Notice.Kind != string(TypeChanged) || len(m.Payload.raw) == 0 {
		return RuleEvent{}, false
	}
	var ev RuleEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return RuleEvent{}, false
	}
	return ev, true
}

// EventAudience returns the audience of a stored event. A post that is not a
// registered event, and every agent message, belongs to the room. A spec that
// names no audience is part of the room's own conversation.
func (m Message) EventAudience() Audience {
	if spec, ok := eventSpecFor(m.Kind, m.Notice.Kind); ok && spec.Audience != "" {
		return spec.Audience
	}
	return AudienceRoom
}

// EngineEvent reports a registered system event. Its sender is the engine, so
// a post that carries the room's own address is still not the root's writing.
func (m Message) EngineEvent() bool {
	_, ok := eventSpecFor(m.Kind, m.Notice.Kind)
	return ok
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

// Review returns the payload of a reviewer result event. It rejects unknown
// fields except the area and note keys stored before those facts were removed.
func (m Message) Review() (ReviewEvent, bool) {
	if m.Kind != string(FamilyReview) || m.Notice.Kind != string(TypeResult) || len(m.Payload.raw) == 0 {
		return ReviewEvent{}, false
	}
	// TODO: legacy/shim remove later — read area and note from old review
	// events; remove these two fields when that history ages out.
	var ev struct {
		ReviewEvent
		Area string `json:"area"`
		Note string `json:"note"`
	}
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Kind == "" {
		return ReviewEvent{}, false
	}
	return ev.ReviewEvent, true
}

// Machine returns the payload of a low-disk machine event. It decodes the
// stored bytes strictly, so an unknown field is a failed read rather than a
// silent mismatch.
func (m Message) Machine() (MachineEvent, bool) {
	if m.Kind != string(FamilyMachine) || m.Notice.Kind != string(TypeLowDisk) || len(m.Payload.raw) == 0 {
		return MachineEvent{}, false
	}
	var ev MachineEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return MachineEvent{}, false
	}
	return ev, true
}

// MachineCache returns the payload of a cache-repair machine event. It decodes
// the stored bytes strictly, so an unknown field is a failed read rather than
// a silent mismatch.
func (m Message) MachineCache() (MachineCacheEvent, bool) {
	if m.Kind != string(FamilyMachine) || m.Notice.Kind != string(TypeCacheRepaired) || len(m.Payload.raw) == 0 {
		return MachineCacheEvent{}, false
	}
	var ev MachineCacheEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return MachineCacheEvent{}, false
	}
	return ev, true
}

// Session returns the payload of a session note.
func (m Message) Session() (SessionEvent, bool) {
	if m.Kind != string(FamilySession) || len(m.Payload.raw) == 0 {
		return SessionEvent{}, false
	}
	var ev SessionEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Type == "" {
		return SessionEvent{}, false
	}
	return ev, true
}

// InstructionBatch returns the payload of a shared instruction-change summary.
func (m Message) InstructionBatch() (InstructionBatchEvent, bool) {
	if m.Kind != string(FamilySession) || m.Notice.Kind != string(TypeInstructionBatch) || len(m.Payload.raw) == 0 {
		return InstructionBatchEvent{}, false
	}
	var ev InstructionBatchEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || len(ev.Sections) == 0 || len(ev.Sessions) == 0 || len(ev.Notes) == 0 {
		return InstructionBatchEvent{}, false
	}
	return ev, true
}

// InstructionNote reports a standing-instruction note: the session event that
// tells one session its instructions changed. One edit reaches every session,
// so a list that shows these notes folds them; it asks the registry for the
// audience and the wake rule instead of matching a kind string.
func (m Message) InstructionNote() (SessionEvent, bool) {
	ev, ok := m.Session()
	if !ok || ev.Type != SessionInstructions || m.EventAudience() != AudienceSession {
		return SessionEvent{}, false
	}
	// The note wakes nobody: its session reads it as turn input.
	if spec, ok := eventSpecFor(m.Kind, m.Notice.Kind); !ok || spec.Wake != WakeNever {
		return SessionEvent{}, false
	}
	return ev, true
}

// Alert returns the payload of an alert.
func (m Message) Alert() (AlertEvent, bool) {
	if m.Kind != string(FamilyAlert) || len(m.Payload.raw) == 0 {
		return AlertEvent{}, false
	}
	var ev AlertEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Source == "" {
		return AlertEvent{}, false
	}
	return ev, true
}

// ServiceFailed returns the payload of a failed service turn.
func (m Message) ServiceFailed() (ServiceFailedEvent, bool) {
	if m.Kind != string(FamilyService) || m.Notice.Kind != string(TypeFailed) || len(m.Payload.raw) == 0 {
		return ServiceFailedEvent{}, false
	}
	var ev ServiceFailedEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Service == "" {
		return ServiceFailedEvent{}, false
	}
	return ev, true
}

// HelperOutcome returns the payload of an automatic helper outcome warning.
func (m Message) HelperOutcome() (HelperOutcomeEvent, bool) {
	if m.Kind != string(FamilyService) || m.Notice.Kind != string(TypeHelperOutcome) || len(m.Payload.raw) == 0 {
		return HelperOutcomeEvent{}, false
	}
	var ev HelperOutcomeEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Reason == "" {
		return HelperOutcomeEvent{}, false
	}
	return ev, true
}

// Engine returns the payload of a note from the engine itself.
func (m Message) Engine() (EngineEvent, bool) {
	if m.Kind != string(FamilyEngine) || m.Notice.Kind != string(TypeRootFlush) || len(m.Payload.raw) == 0 {
		return EngineEvent{}, false
	}
	var ev EngineEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil {
		return EngineEvent{}, false
	}
	return ev, true
}

// Call returns the payload of an answer to a call request. It decodes the
// stored bytes strictly, so an unknown field is a failed read rather than a
// silent mismatch.
func (m Message) Call() (CallEvent, bool) {
	if m.Kind != string(FamilyCall) || len(m.Payload.raw) == 0 {
		return CallEvent{}, false
	}
	var ev CallEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Call == "" {
		return CallEvent{}, false
	}
	return ev, true
}

// Question returns the payload of a note about a question.
func (m Message) Question() (QuestionEvent, bool) {
	if m.Kind != string(FamilyQuestion) || m.Notice.Kind != string(TypeWithdrawn) || len(m.Payload.raw) == 0 {
		return QuestionEvent{}, false
	}
	var ev QuestionEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.QuestionID == "" {
		return QuestionEvent{}, false
	}
	return ev, true
}
