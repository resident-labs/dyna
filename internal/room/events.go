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

// WakeNever posts an event without starting or steering any session. Later
// PRs add the rules for events that do wake someone.
const WakeNever WakeRule = "never"

// Event families and types.
const (
	FamilyWorktree EventFamily = "worktree"
	TypeKept       EventType   = "kept"
)

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
var eventSpecs = []EventSpec{{
	Family:   FamilyWorktree,
	Type:     TypeKept,
	Renderer: "WorktreeCard",
	Payload:  reflect.TypeOf(WorktreeEvent{}),
	Quiet:    true,
	Wake:     WakeNever,
	Once:     true,
	Text:     "Worktree kept at {path}: branch {branch}, ahead {ahead}, dirty: {dirty}. {detail}",
	// The keep failure names no tree.
	TextNoSubject: "Worktree cleanup could not keep the session's trees: {detail}",
}}

// eventEnum is a named string type whose values the web needs as a union.
type eventEnum struct {
	Type   reflect.Type
	Values []string
}

var eventEnums = []eventEnum{
	{Type: reflect.TypeOf(WorktreeReason("")), Values: []string{
		string(WorktreeNotMerged), string(WorktreeDirty), string(WorktreeManualHandoff), string(WorktreeKeepFailed), string(WorktreeOther),
	}},
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
	"hook-failed",
	"instructions",
	"issue",
	"merge-decision",
	"notice",
	"pr",
	"seat",
	"spawn",
	"test-health",
	"voice",
}

// agentKinds are the posts a person or an agent writes, and the session turn
// results the engine records for them.
var agentKinds = []string{"message", "completed", "failed", "stopped"}

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

// PostEvent stores one system event. It looks up the spec, rejects an event
// whose required fields are empty, fixes the sender to the engine, derives the
// post ID from the event so the same event is stored once, and applies the
// spec's quiet and wake rules. A spec with Once drops a later post with the
// same key: it returns no message and no error.
func (s *Store) PostEvent(roomID string, ev Event, to ...string) (Message, error) {
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
	text := spec.Text
	if ev.eventSubject() == "" {
		text = spec.TextNoSubject
	}
	msg, err := s.post(roomID, PostInput{
		ID:      eventID(ev),
		From:    EngineSender,
		Kind:    string(spec.Family),
		Notice:  PRNotice{Kind: string(spec.Type)},
		payload: EventPayload{raw: payload},
		To:      to,
		Text:    renderEventText(text, ev),
		Wake:    spec.Wake != WakeNever,
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

// eventID is the post ID of an event: family, type, subject and the event's
// own dedupe key. The same event in a later pass gets the same ID.
func eventID(ev Event) string {
	key := strings.Join([]string{string(ev.eventFamily()), string(ev.eventType()), ev.eventSubject(), ev.eventDedupe()}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("msg_%x", sum[:8])
}

var eventPlaceholder = regexp.MustCompile(`\{([a-z]+)\}`)

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
			items := make([]string, fieldValue.Len())
			for j := range items {
				items[j] = fieldValue.Index(j).String()
			}
			return strings.Join(items, ", ")
		}
	}
	return ""
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
