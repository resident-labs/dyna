package subagent

import (
	"bytes"
	"encoding/json"
	"errors"
)

// JournalKind is the "t" value of a line in a session's events.jsonl: the one
// fact the line records.
type JournalKind string

// Journal kinds. Only the engine and its turn runners write a session journal,
// so its kinds are a finite list and each one has a named payload (rule r7).
const (
	// JournalSessionStart records the session a start call created.
	JournalSessionStart JournalKind = "session_start"
	// JournalSessionClosed records a close and what it handed over.
	JournalSessionClosed JournalKind = "session_closed"
	// JournalSessionReopened records a consumed session that reopened.
	JournalSessionReopened JournalKind = "session_reopened"
	// JournalSessionDormant records a session that ended a turn with no work
	// left and went dormant.
	JournalSessionDormant JournalKind = "session_dormant"
	// JournalSessionPromoted and JournalSessionReparented record a session
	// that changed its place in the hierarchy, to a new parent or to none.
	JournalSessionPromoted   JournalKind = "session_promoted"
	JournalSessionReparented JournalKind = "session_reparented"
	// JournalTurnStart records a turn that opened.
	JournalTurnStart JournalKind = "turn_start"
	// JournalTurnEnd records how a turn ended.
	JournalTurnEnd JournalKind = "turn_end"
	// JournalTurnRepaired records a terminal turn that a later call repaired.
	JournalTurnRepaired JournalKind = "turn_repaired"
	// JournalRoomDrained records one room post a turn drained as its steer.
	JournalRoomDrained JournalKind = "room_drained"
	// JournalSteerSubmitted records a post handed to a running turn.
	JournalSteerSubmitted JournalKind = "steer_submitted"
	// JournalSteerDelivered records that the harness read the post it was sent.
	JournalSteerDelivered JournalKind = "steer_delivered"
	// JournalDefer records a delivery the runner deferred.
	JournalDefer JournalKind = "defer"
	// JournalModelFallback records a turn that moved to another model.
	JournalModelFallback JournalKind = "model_fallback"
	// JournalHarnessSession records the conversation the harness created.
	JournalHarnessSession JournalKind = "harness_session"
	// JournalCompactFailed records a compaction that failed.
	JournalCompactFailed JournalKind = "compact_failed"
	// JournalRuntimeRepin records a session moved to the installed runtime.
	JournalRuntimeRepin JournalKind = "runtime_repin"
	// JournalTurnEmpty records a woken turn that found no input.
	JournalTurnEmpty JournalKind = "turn_empty"
	// JournalForgedDelivery records a reply that imitated a Dyna delivery.
	JournalForgedDelivery JournalKind = "forged_delivery"
	// JournalWorktreeReportFailed records a kept-tree report the room refused.
	JournalWorktreeReportFailed JournalKind = "worktree_cleanup_report_failed"
	// JournalCascadeCloseFailed records a child a cascade close could not close.
	JournalCascadeCloseFailed JournalKind = "cascade_close_failed"
	// JournalChildHandoverFailed records a close that could not hand its
	// children to an heir.
	JournalChildHandoverFailed JournalKind = "child_handover_failed"
)

// JournalPayload is the typed payload of one journal kind. Its method is
// unexported, so only this package declares a payload and a writer cannot
// invent a kind from a map of string keys. The JSON tags keep the field names
// the journal has always written, so an old line still decodes.
type JournalPayload interface {
	journalKind() JournalKind
}

// SessionStartJournal is what a start call writes: what the session is and
// where it runs.
type SessionStartJournal struct {
	Profile string `json:"profile"`
	Depth   int    `json:"depth"`
	Parent  string `json:"parent"`
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Room    string `json:"room"`
	Address string `json:"address"`
	Dir     string `json:"dir"`
	Cwd     string `json:"cwd"`
}

func (SessionStartJournal) journalKind() JournalKind { return JournalSessionStart }

// SessionClosedJournal is what a close writes: who closed the session and the
// summary lines it recorded, such as the heir it chose.
type SessionClosedJournal struct {
	By      string   `json:"by"`
	Handoff []string `json:"handoff"`
}

func (SessionClosedJournal) journalKind() JournalKind { return JournalSessionClosed }

// SessionReopenedJournal records a consumed session that reopened.
type SessionReopenedJournal struct{}

func (SessionReopenedJournal) journalKind() JournalKind { return JournalSessionReopened }

// SessionDormantJournal records a session that went dormant.
type SessionDormantJournal struct{}

func (SessionDormantJournal) journalKind() JournalKind { return JournalSessionDormant }

// SessionMovedJournal is the payload of both kinds that change a session's
// place in the hierarchy: a promotion, and a reparent that keeps the depth.
type SessionMovedJournal struct {
	PreviousParent string `json:"previousParent"`
	PreviousDepth  int    `json:"previousDepth"`
	Parent         string `json:"parent"`
	Depth          int    `json:"depth"`
}

// SessionPromotedJournal records a session that gained a parent.
type SessionPromotedJournal struct {
	SessionMovedJournal
}

func (SessionPromotedJournal) journalKind() JournalKind { return JournalSessionPromoted }

// SessionReparentedJournal records a session that moved to another parent.
type SessionReparentedJournal struct {
	SessionMovedJournal
}

func (SessionReparentedJournal) journalKind() JournalKind { return JournalSessionReparented }

// TurnStartJournal records a turn that opened.
type TurnStartJournal struct {
	Turn int `json:"turn"`
}

func (TurnStartJournal) journalKind() JournalKind { return JournalTurnStart }

// TurnEndJournal records how a turn ended, and why its row was not written
// when that happened, so a later repair is explained.
type TurnEndJournal struct {
	Turn   int    `json:"turn"`
	Status string `json:"status"`
	Error  string `json:"error"`
	// WriteError says why the turn row was not saved. The line is appended
	// either way.
	WriteError string `json:"writeError,omitempty"`
	// Preview is the first 240 characters of the turn's result.
	Preview string `json:"preview,omitempty"`
}

func (TurnEndJournal) journalKind() JournalKind { return JournalTurnEnd }

// TurnRepairedJournal records what the repair read and who asked for it: a
// runner that died leaves no other trace of the turn it was running.
type TurnRepairedJournal struct {
	Turn         int    `json:"turn"`
	ReadStatus   string `json:"readStatus"`
	ReadRevision string `json:"readRevision"`
	GatePid      int    `json:"gatePid"`
	GateTurn     int    `json:"gateTurn"`
	GateActive   bool   `json:"gateActive"`
	Pid          int    `json:"pid"`
	Process      string `json:"process"`
	Caller       string `json:"caller"`
}

func (TurnRepairedJournal) journalKind() JournalKind { return JournalTurnRepaired }

// RoomDrainedJournal records one room post a turn drained as its steer.
type RoomDrainedJournal struct {
	Turn    int    `json:"turn"`
	Message string `json:"message"`
}

func (RoomDrainedJournal) journalKind() JournalKind { return JournalRoomDrained }

// SteerSubmittedJournal records a live post handed to a running turn, and
// whether the session was still queued.
type SteerSubmittedJournal struct {
	Turn   int  `json:"turn"`
	Queued bool `json:"queued"`
}

func (SteerSubmittedJournal) journalKind() JournalKind { return JournalSteerSubmitted }

// SteerDeliveredJournal records that the harness read the post it was sent.
type SteerDeliveredJournal struct {
	Turn int `json:"turn"`
}

func (SteerDeliveredJournal) journalKind() JournalKind { return JournalSteerDelivered }

// DeferJournal records a delivery the runner deferred, and how long it waited.
type DeferJournal struct {
	Turn     int    `json:"turn"`
	Kind     string `json:"kind"`
	Outcome  string `json:"outcome"`
	WaitedMs int64  `json:"waitedMs"`
}

func (DeferJournal) journalKind() JournalKind { return JournalDefer }

// ModelFallbackJournal records a turn that moved from one model to another.
type ModelFallbackJournal struct {
	Turn    int    `json:"turn"`
	From    string `json:"from"`
	To      string `json:"to"`
	Message string `json:"message"`
}

func (ModelFallbackJournal) journalKind() JournalKind { return JournalModelFallback }

// HarnessSessionJournal records the conversation the harness created for the
// session.
type HarnessSessionJournal struct {
	Turn    int    `json:"turn"`
	Session string `json:"session"`
}

func (HarnessSessionJournal) journalKind() JournalKind { return JournalHarnessSession }

// CompactFailedJournal records a compaction that failed during a turn.
type CompactFailedJournal struct {
	Turn    int    `json:"turn"`
	Message string `json:"message"`
}

func (CompactFailedJournal) journalKind() JournalKind { return JournalCompactFailed }

// RuntimeRepinJournal records a session that now uses the installed version,
// because the private bundle it ran on was removed.
type RuntimeRepinJournal struct {
	Message string `json:"message"`
	Version string `json:"version"`
}

func (RuntimeRepinJournal) journalKind() JournalKind { return JournalRuntimeRepin }

// TurnEmptyJournal records a woken turn that found no input.
type TurnEmptyJournal struct {
	Turn int `json:"turn"`
}

func (TurnEmptyJournal) journalKind() JournalKind { return JournalTurnEmpty }

// WorktreeReportFailedJournal records a kept-tree report the room refused. The
// trees stay kept; only the post failed.
type WorktreeReportFailedJournal struct {
	Error string `json:"error"`
}

func (WorktreeReportFailedJournal) journalKind() JournalKind {
	return JournalWorktreeReportFailed
}

// CascadeCloseFailedJournal records a child a cascade close could not close.
type CascadeCloseFailedJournal struct {
	Parent string `json:"parent"`
	Error  string `json:"error"`
}

func (CascadeCloseFailedJournal) journalKind() JournalKind { return JournalCascadeCloseFailed }

// ChildHandoverFailedJournal records a close that could not hand its children
// to an heir.
type ChildHandoverFailedJournal struct {
	Error string `json:"error"`
}

func (ChildHandoverFailedJournal) journalKind() JournalKind {
	return JournalChildHandoverFailed
}

// UnknownJournal is a line of a kind this version does not know, as a newer
// engine writes it. It keeps the line as it was, so an older reader shows the
// fact instead of dropping it. This is the journal's only untyped payload.
type UnknownJournal struct {
	Kind JournalKind `json:"-"`
	// Raw is the whole line, envelope included.
	Raw json.RawMessage `json:"-"`
}

func (u UnknownJournal) journalKind() JournalKind { return u.Kind }

// JournalEvent is one line of a session's events.jsonl: a kind, its typed
// payload and the stamp the engine gave it. On disk the payload's fields sit
// beside the envelope, as {"t":"turn_start","turn":1,"ts":1730000000000}, the
// shape every journal line has had.
type JournalEvent struct {
	TS      int64
	Payload JournalPayload
}

// MarshalJSON writes the kind, the payload's fields and the stamp as one flat
// object. The unknown kind keeps its own line, because this version cannot
// rebuild one.
func (e JournalEvent) MarshalJSON() ([]byte, error) {
	if e.Payload == nil {
		return nil, errors.New("a journal event needs a payload")
	}
	if unknown, ok := e.Payload.(*UnknownJournal); ok {
		return unknown.Raw, nil
	}
	body, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	kind, err := json.Marshal(e.Payload.journalKind())
	if err != nil {
		return nil, err
	}
	buf.WriteString(`"t":`)
	buf.Write(kind)
	// The payload marshals to an object, so its brace pair is dropped and its
	// fields join the envelope.
	if len(body) > 2 {
		buf.WriteByte(',')
		buf.Write(body[1 : len(body)-1])
	}
	stamp, err := json.Marshal(e.TS)
	if err != nil {
		return nil, err
	}
	buf.WriteString(`,"ts":`)
	buf.Write(stamp)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads the kind, the payload and the stamp, and keeps an
// unknown kind's line whole.
func (e *JournalEvent) UnmarshalJSON(data []byte) error {
	var env struct {
		Kind JournalKind `json:"t"`
		TS   int64       `json:"ts"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	payload, known, err := decodeJournalPayload(env.Kind, data)
	if err != nil {
		return err
	}
	if !known {
		payload = &UnknownJournal{Kind: env.Kind, Raw: append(json.RawMessage(nil), data...)}
	}
	e.TS, e.Payload = env.TS, payload
	return nil
}

// decodeJournalPayload decodes a whole line into the payload struct of its
// kind. It reports whether this version knows the kind.
func decodeJournalPayload(kind JournalKind, line []byte) (JournalPayload, bool, error) {
	payload, ok := newJournalPayload(kind)
	if !ok {
		return nil, false, nil
	}
	fields, err := payloadFields(line)
	if err != nil {
		return nil, true, err
	}
	// Strict, like the sealed room events, so an unknown field in a known kind
	// is a failed read rather than a silent mismatch.
	decoder := json.NewDecoder(bytes.NewReader(fields))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return nil, true, err
	}
	return payload, true, nil
}

// payloadFields returns the line without its "t" and "ts" envelope keys, in
// the form a payload struct decodes.
func payloadFields(line []byte) ([]byte, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(line, &all); err != nil {
		return nil, err
	}
	delete(all, "t")
	delete(all, "ts")
	return json.Marshal(all)
}

// newJournalPayload returns an empty payload of kind, which a read fills from
// its line. It reports false for a kind this version does not know.
func newJournalPayload(kind JournalKind) (JournalPayload, bool) {
	switch kind {
	case JournalSessionStart:
		return &SessionStartJournal{}, true
	case JournalSessionClosed:
		return &SessionClosedJournal{}, true
	case JournalSessionReopened:
		return &SessionReopenedJournal{}, true
	case JournalSessionDormant:
		return &SessionDormantJournal{}, true
	case JournalSessionPromoted:
		return &SessionPromotedJournal{}, true
	case JournalSessionReparented:
		return &SessionReparentedJournal{}, true
	case JournalTurnStart:
		return &TurnStartJournal{}, true
	case JournalTurnEnd:
		return &TurnEndJournal{}, true
	case JournalTurnRepaired:
		return &TurnRepairedJournal{}, true
	case JournalRoomDrained:
		return &RoomDrainedJournal{}, true
	case JournalSteerSubmitted:
		return &SteerSubmittedJournal{}, true
	case JournalSteerDelivered:
		return &SteerDeliveredJournal{}, true
	case JournalDefer:
		return &DeferJournal{}, true
	case JournalModelFallback:
		return &ModelFallbackJournal{}, true
	case JournalHarnessSession:
		return &HarnessSessionJournal{}, true
	case JournalCompactFailed:
		return &CompactFailedJournal{}, true
	case JournalRuntimeRepin:
		return &RuntimeRepinJournal{}, true
	case JournalTurnEmpty:
		return &TurnEmptyJournal{}, true
	case JournalForgedDelivery:
		return &ForgedDeliveryJournal{}, true
	case JournalWorktreeReportFailed:
		return &WorktreeReportFailedJournal{}, true
	case JournalCascadeCloseFailed:
		return &CascadeCloseFailedJournal{}, true
	case JournalChildHandoverFailed:
		return &ChildHandoverFailedJournal{}, true
	}
	return nil, false
}
