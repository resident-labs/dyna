package room

import (
	"reflect"
	"slices"
)

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
	// A note is evidence added to an open or closed issue. Each note is its own
	// post, so it has no Once key, and it reaches the room's root quietly.
	{
		Family:   FamilyIssue,
		Type:     EventType(IssueNoted),
		Renderer: "IssueCard",
		Payload:  reflect.TypeOf(IssueEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Text:     "{actor} added a note to issue D-{number}: {title}",
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
	// A failed cache cleanup names one path and one bounded reason. An hourly
	// retry of the same failure reuses this post instead of adding another.
	{
		Family:   FamilyMachine,
		Type:     TypeCacheCleanFailed,
		Renderer: "MachineCacheCleanCard",
		Payload:  reflect.TypeOf(MachineCacheCleanFailedEvent{}),
		Wake:     WakeRootIfNeeded,
		Once:     true,
		Text:     "Machine {name} could not clean CI cache path {path}: {reason}.",
	},
	// A sweep of a machine's scratch folders that freed space posts one quiet
	// machine fact, so rooms that use the machine see what went and why.
	{
		Family:   FamilyMachine,
		Type:     TypeScratchFreed,
		Renderer: "MachineScratchCard",
		Payload:  reflect.TypeOf(MachineScratchEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Machine {name} freed scratch space: {summary}.",
	},
	// A machine that cannot run one slice per room tells each room once per
	// cause: the room's work then shares the agent slice with no process cap.
	{
		Family:   FamilyMachine,
		Type:     TypeLimitsOff,
		Renderer: "MachineLimitsCard",
		Payload:  reflect.TypeOf(MachineLimitsEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "Room limits are off: {reason}. This room shares the agent slice with every other room and has no process cap.",
	},
	// A room whose work nears one cap of its resource slice hears it once per
	// cap and crossing: its root can rein the room in before the kernel
	// throttles its memory or refuses its forks.
	{
		Family:   FamilyMachine,
		Type:     TypeRoomCap,
		Renderer: "RoomCapCard",
		Payload:  reflect.TypeOf(RoomCapEvent{}),
		Wake:     WakeRoot,
		Once:     true,
		Text:     "Room {room} is near its {cap} cap: {summary}.",
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
	// A voice session notice wakes nobody: it tells the owner who is using the
	// voice quota, and the agent in the session already knows.
	{
		Family:   FamilyVoice,
		Type:     EventType(VoiceStarted),
		Renderer: "VoiceSessionCard",
		Payload:  reflect.TypeOf(VoiceSessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{session} started a live voice session on {provider}.",
	},
	{
		Family:   FamilyVoice,
		Type:     EventType(VoiceEnded),
		Renderer: "VoiceSessionCard",
		Payload:  reflect.TypeOf(VoiceSessionEvent{}),
		Quiet:    true,
		Wake:     WakeNever,
		Once:     true,
		Text:     "{session} ended its live voice session on {provider} after {seconds} seconds: {reason}.",
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
	// A computer reminder steers a running session, or wakes an idle one, to
	// look at its open computer. Each reminder is its own post.
	{
		Family:   FamilySession,
		Type:     EventType(SessionComputer),
		Renderer: "SessionNoteCard",
		Payload:  reflect.TypeOf(SessionEvent{}),
		Wake:     WakeTarget,
		Audience: AudienceSession,
		Text:     "Your computer is open (apps: {apps}). Run `dyna computer shot` to check it, and `dyna computer stop --status \"...\"` when you are done.",
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
		Text:      "The engine is read-only because migration failed:",
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
	{
		Family: FamilyAlert, Type: EventType(AlertShell), Renderer: "AlertCard",
		Payload: reflect.TypeOf(AlertEvent{}), Wake: WakeTarget, Audience: AudienceSession, Once: true,
		Text: "Shell process cleanup did not finish:", Producers: []EventProducer{ProducerShell},
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
	// A pinned service that cannot run its pin waits. Root hears it once per
	// pin and reason, and the service does not start on another profile.
	{
		Family:    FamilyService,
		Type:      TypeWaiting,
		Renderer:  "ServiceWaitCard",
		Payload:   reflect.TypeOf(ServiceWaitEvent{}),
		Wake:      WakeRoot,
		Once:      true,
		Text:      "The {service} service is waiting. Its pinned profile {profile} cannot run: {reason}",
		Producers: []EventProducer{ProducerLibrarian, ProducerReviewer},
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
	// A terminal request wakes the session that holds it. A request that waits
	// for the owner wakes no session; the poster turns the wake off.
	{
		Family:   FamilyTerminal,
		Type:     TypeRequest,
		Renderer: "TerminalRequestCard",
		Payload:  reflect.TypeOf(TerminalRequestEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Text:     "Terminal request {requestId} from {asker}: {reason}",
	},
	// The owner's report is the asking session's answer, so it wakes that
	// session. A withdrawal is quiet; the poster turns the wake off.
	{
		Family:   FamilyTerminal,
		Type:     TypeRequestDone,
		Renderer: "TerminalRequestCard",
		Payload:  reflect.TypeOf(TerminalDoneEvent{}),
		Wake:     WakeTarget,
		Once:     true,
		Audience: AudienceSession,
		Blocks:   []string{"detail", "note", "sharedLines"},
		Text:     "Terminal request {requestId} ended: {verdict}.",
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
	{
		Family:            FamilyShell,
		Type:              TypeShellSilence,
		Renderer:          "ShellSilenceCard",
		Payload:           reflect.TypeOf(ShellSilenceEvent{}),
		Wake:              WakeTarget,
		Once:              true,
		RootOnOwnerReport: true,
		Text:              "Shell job {label} has no output log growth for {minutes} minutes. Partial lines may be buffered; this does not mean the job is stuck.",
		Producers:         []EventProducer{ProducerShell},
	},
	// A job's life belongs to the session that submitted it: the queued wait
	// and the start are quiet, the result wakes the session like a shell
	// result does. All stay out of the room watch; the session DM and the job
	// card carry them.
	{
		Family:    FamilyJob,
		Type:      TypeJobQueued,
		Renderer:  "JobCard",
		Payload:   reflect.TypeOf(JobEvent{}),
		Quiet:     true,
		NoWatch:   true,
		Wake:      WakeNever,
		Once:      true,
		Audience:  AudienceSession,
		Blocks:    []string{"detail", "waitReason"},
		Text:      "Job {label} waits on {node}: {waitReason}.",
		Producers: []EventProducer{ProducerJobs},
	},
	{
		Family:    FamilyJob,
		Type:      TypeStarted,
		Renderer:  "JobCard",
		Payload:   reflect.TypeOf(JobEvent{}),
		Quiet:     true,
		NoWatch:   true,
		Wake:      WakeNever,
		Once:      true,
		Audience:  AudienceSession,
		Blocks:    []string{"detail"},
		Text:      "Job {label} started on {node}.",
		Producers: []EventProducer{ProducerJobs},
	},
	{
		Family:            FamilyJob,
		Type:              TypeJobFinished,
		Renderer:          "JobCard",
		Payload:           reflect.TypeOf(JobEvent{}),
		NoWatch:           true,
		Wake:              WakeTarget,
		Once:              true,
		RootOnOwnerReport: true,
		Audience:          AudienceSession,
		Blocks:            []string{"detail"},
		Text:              "Job {label} on {node} {state} with exit code {exitCode}.",
		Producers:         []EventProducer{ProducerJobs},
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
	{Type: reflect.TypeOf(JobState("")), Values: enumValues(jobStates)},
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
	{Type: reflect.TypeOf(TerminalVerdict("")), Values: enumValues(terminalVerdicts)},
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
	{Type: reflect.TypeOf(VoiceSessionState("")), Values: enumValues(voiceSessionStates)},
	{Type: reflect.TypeOf(VoiceEndReason("")), Values: enumValues(voiceEndReasons)},
	{Type: reflect.TypeOf(RoomCapKind("")), Values: []string{
		string(RoomCapMemoryHigh), string(RoomCapMemoryMax), string(RoomCapPidsMax),
	}},
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
	reflect.TypeOf(RoomCapTree{}),
}

// eventOpenStrings are named string types whose values are open, such as a
// session address or the engine that made a change. The generator reads each
// as a plain string, because a union of its values would be incomplete.
var eventOpenStrings = []reflect.Type{reflect.TypeOf(IssueActor(""))}

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

// enumValues lists the values of a named string type for the generated web
// union.
func enumValues[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}
