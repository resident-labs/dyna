package room

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
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
	// FamilyTerminal is a session's request for the owner to run a command in
	// a terminal, and how it ended.
	FamilyTerminal  EventFamily = "terminal"
	TypeRequest     EventType   = "request"
	TypeRequestDone EventType   = "request.done"

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
	// TypeScratchFreed is a sweep that freed scratch space on a machine.
	TypeScratchFreed EventType = "scratch-freed"
	// TypeLimitsOff is the machine telling a room it cannot give each room
	// its own resource slice, so the room shares the agent slice instead.
	TypeLimitsOff EventType = "limits-off"

	// FamilyCall reports how the owner answered a call request. The engine
	// posts it, so the answer wears no invented voice and carries no raw ID in
	// its text.
	FamilyCall EventFamily = "call"

	// FamilyVoice tells the room that an agent started or ended a live voice
	// session, so the owner sees who spends the voice provider's quota.
	FamilyVoice EventFamily = "voice-session"

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

// ServiceWait returns the payload of a pinned service that is waiting.
func (m Message) ServiceWait() (ServiceWaitEvent, bool) {
	if m.Kind != string(FamilyService) || m.Notice.Kind != string(TypeWaiting) || len(m.Payload.raw) == 0 {
		return ServiceWaitEvent{}, false
	}
	var ev ServiceWaitEvent
	decoder := json.NewDecoder(bytes.NewReader(m.Payload.raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ev); err != nil || ev.Service == "" || ev.Profile == "" {
		return ServiceWaitEvent{}, false
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

// VoiceSession returns the payload of a live voice session notice.
func (m Message) VoiceSession() (VoiceSessionEvent, bool) {
	if m.Kind != string(FamilyVoice) || len(m.Payload.raw) == 0 {
		return VoiceSessionEvent{}, false
	}
	var ev VoiceSessionEvent
	if err := decodeEventPayload(m.Payload.raw, &ev); err != nil || ev.Voice == "" {
		return VoiceSessionEvent{}, false
	}
	return ev, true
}

// TerminalRequest returns the payload of a filed terminal request.
func (m Message) TerminalRequest() (TerminalRequestEvent, bool) {
	if m.Kind != string(FamilyTerminal) || m.Notice.Kind != string(TypeRequest) || len(m.Payload.raw) == 0 {
		return TerminalRequestEvent{}, false
	}
	var ev TerminalRequestEvent
	if err := decodeEventPayload(m.Payload.raw, &ev); err != nil || ev.RequestID == "" {
		return TerminalRequestEvent{}, false
	}
	return ev, true
}

// TerminalDone returns the payload of a finished terminal request.
func (m Message) TerminalDone() (TerminalDoneEvent, bool) {
	if m.Kind != string(FamilyTerminal) || m.Notice.Kind != string(TypeRequestDone) || len(m.Payload.raw) == 0 {
		return TerminalDoneEvent{}, false
	}
	var ev TerminalDoneEvent
	if err := decodeEventPayload(m.Payload.raw, &ev); err != nil || ev.RequestID == "" {
		return TerminalDoneEvent{}, false
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
