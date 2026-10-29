package room

import (
	"strings"
	"testing"
)

// The session notes, alerts, service alerts, engine notes and question notes
// are sealed families: only PostEvent stores them, and a free-text post of one
// is a programming error.
func TestPostRejectsSessionAlertServiceEngineAndQuestionFamilies(t *testing.T) {
	store, rec := newEventStore(t)
	for _, family := range []EventFamily{FamilySession, FamilyAlert, FamilyService, FamilyEngine, FamilyQuestion} {
		if _, err := store.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Kind: string(family), Text: "sealed"}); err == nil || !strings.Contains(err.Error(), "PostEvent") {
			t.Fatalf("Post accepted the sealed family %q: %v", family, err)
		}
	}
}

// wakeCase is one typed event and the delivery rule the free-text post it
// replaces had.
type wakeCase struct {
	event Event
	// to is the recipient address (the room address when empty).
	to string
	// needsRoot marks a post that asks the root seat to act on it.
	needsRoot bool
	producer  EventProducer
	// wakes is whether the stored post starts the recipient's turn.
	wakes bool
	// wakesRoot is whether the post reaches the root seat as a wake.
	wakesRoot bool
	// quiet marks an event that is not agent input at all.
	quiet    bool
	audience Audience
}

// The new events keep the routing of the free-text posts they replace: notes
// are quiet, alerts wake the root seat, and only a seat handoff wakes.
func TestNewEventWakeAndRecipientRules(t *testing.T) {
	const session = "work:kid"
	const toSession = session
	const toRoot = "work:root"
	cases := map[string]wakeCase{
		"session/renamed": {
			event:    SessionEvent{Type: SessionRenamed, Session: session, OldName: "kid", Name: "kid-new"},
			to:       toRoot,
			quiet:    true,
			audience: AudienceSession,
		},
		"session/promoted": {
			event:    SessionEvent{Type: SessionPromoted, Session: session, From: "work:lead", To: "top level"},
			to:       toRoot,
			quiet:    true,
			audience: AudienceSession,
		},
		"session/adopted": {
			event:    SessionEvent{Type: SessionAdopted, Session: session, From: "top level", To: "work:lead"},
			to:       toRoot,
			quiet:    true,
			audience: AudienceSession,
		},
		"session/account": {
			event:    SessionEvent{Type: SessionAccount, Session: session, From: "claude", To: "claude-2", Reason: AccountUsageLimit},
			to:       toRoot,
			quiet:    true,
			audience: AudienceRoom,
		},
		"session/instructions": {
			event:    SessionEvent{Type: SessionInstructions, Session: session, Sections: []InstructionChange{{Name: "Room rules", Action: InstructionItems, Added: []string{"Rule 4"}}}, Detail: "- Room rules: added \"Rule 4\"."},
			to:       toSession,
			audience: AudienceSession,
		},
		"session/seat": {
			event:    SessionEvent{Type: SessionSeat, Session: "work", Seat: "Internal root took back the seat"},
			to:       toRoot,
			audience: AudienceRoom,
		},
		"session/seat with handoff": {
			event:     SessionEvent{Type: SessionSeat, Session: "work", Seat: "Internal root took back the seat", Detail: "handoff"},
			to:        toRoot,
			needsRoot: true,
			wakes:     true,
			wakesRoot: true,
			audience:  AudienceRoom,
		},
		"session/spawned": {
			event:    SessionEvent{Type: SessionSpawned, Session: session, Name: "kid", Profile: "deepseek", Dir: "/repo", Detail: "Fix the test"},
			to:       "work",
			quiet:    true,
			audience: AudienceRoom,
		},
		"alert/doctor": {
			event:     AlertEvent{Source: AlertDoctor, ReportID: "7", Trigger: "on demand", Summary: "machine is overloaded", Detail: "machine is overloaded"},
			to:        toRoot,
			producer:  ProducerDoctor,
			wakes:     true,
			wakesRoot: true,
			audience:  AudienceRoom,
		},
		"alert/engine": {
			event:     AlertEvent{Source: AlertEngine, Summary: "The engine is read-only because migration failed.", Detail: "busy"},
			to:        "work",
			producer:  ProducerEngine,
			wakes:     true,
			wakesRoot: true,
			audience:  AudienceRoom,
		},
		"alert/shell": {
			event: AlertEvent{Source: AlertShell, Summary: "Shell process cleanup did not finish.", Detail: "cleanup failed"},
			to:    toSession, producer: ProducerShell, wakes: true, audience: AudienceSession,
		},
		"service/failed": {
			event:     ServiceFailedEvent{Service: ServiceLibrarian, Session: session, Turn: 3, Error: "exit status 1", Profile: "deepseek", Model: "deepseek-flash"},
			to:        toRoot,
			producer:  ProducerLibrarian,
			wakes:     true,
			wakesRoot: true,
			audience:  AudienceRoom,
		},
		"engine/root-flush": {
			event:     EngineEvent{Minutes: 15},
			to:        "work",
			needsRoot: true,
			wakes:     true,
			wakesRoot: true,
			audience:  AudienceRoom,
		},
		"question/withdrawn": {
			event:    QuestionEvent{QuestionID: "q1", Post: "msg_0000000000000001", By: session, Reason: "session closed"},
			to:       toSession,
			quiet:    true,
			audience: AudienceRoom,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			store, rec := newEventStore(t)
			to := c.to
			if to == "work" || to == "work:root" || to == "work:kid" {
				to = strings.Replace(to, "work", rec.Name, 1)
			}
			if c.to == "" {
				to = rec.Name
			}
			msg, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: c.event, Producer: c.producer, NeedsRoot: c.needsRoot, To: []string{to}})
			if err != nil {
				t.Fatal(err)
			}
			if msg.Wake != c.wakes {
				t.Fatalf("stored wake = %v, want %v", msg.Wake, c.wakes)
			}
			if got := rootWakes(msg, rec.Name); got != c.wakesRoot {
				t.Fatalf("rootWakes = %v, want %v", got, c.wakesRoot)
			}
			if got := !msg.Deliverable(); got != c.quiet {
				t.Fatalf("quiet = %v, want %v", got, c.quiet)
			}
			if got := msg.EventAudience(); got != c.audience {
				t.Fatalf("audience = %q, want %q", got, c.audience)
			}
			if msg.From != EngineSender {
				t.Fatalf("sender = %q, want %q", msg.From, EngineSender)
			}
			if spec, ok := eventSpecFor(msg.Kind, msg.Notice.Kind); !ok || spec.Quiet != c.quiet {
				t.Fatalf("spec = %+v, %v", spec, ok)
			}
		})
	}
}

// An event that wakes the root seat is addressed to that seat and no one else.
func TestWakeRootEventRejectsASessionRecipient(t *testing.T) {
	store, rec := newEventStore(t)
	event := AlertEvent{Source: AlertDoctor, ReportID: "7", Summary: "machine is overloaded", Detail: "machine is overloaded"}
	if _, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: event, Producer: ProducerDoctor, To: []string{rec.Name + ":kid"}}); err == nil || !strings.Contains(err.Error(), "root seat") {
		t.Fatalf("alert to a session = %v", err)
	}
	if _, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: event, Producer: ProducerDoctor}); err == nil {
		t.Fatal("alert without a recipient was stored")
	}
}

// A service alert names the service that posts it, and only that service.
func TestServiceFailedEventRequiresItsProducer(t *testing.T) {
	store, rec := newEventStore(t)
	event := ServiceFailedEvent{Service: ServiceReviewer, Session: "work:reviewer", Turn: 2, Error: "turn failed"}
	for name, producer := range map[string]EventProducer{"none": "", "another family": ProducerDoctor} {
		if _, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: event, Producer: producer, To: []string{rec.Name}}); err == nil {
			t.Fatalf("%s producer was accepted", name)
		}
	}
	msg, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: event, Producer: ProducerReviewer, To: []string{rec.Name}})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := msg.ServiceFailed()
	if !ok || payload.Service != ServiceReviewer || payload.Turn != 2 || payload.Error != "turn failed" {
		t.Fatalf("payload = %+v, %v", payload, ok)
	}
	// One alert per service turn, however often the failure is reported.
	again, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: event, Producer: ProducerReviewer, To: []string{rec.Name}})
	if err != nil || again.ID != msg.ID {
		t.Fatalf("second report of one turn = %+v, %v", again, err)
	}
}

// Each new event decodes back from its stored post.
func TestNewEventPayloadsRoundTrip(t *testing.T) {
	store, rec := newEventStore(t)
	session := SessionEvent{Type: SessionRenamed, Session: "work:kid", OldName: "kid", Name: "kid-new"}
	msg, err := store.PostEvent(rec.ID, session, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if payload, ok := msg.Session(); !ok || payload.Name != "kid-new" || payload.OldName != "kid" {
		t.Fatalf("session payload = %+v, %v", payload, ok)
	}
	alert, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: AlertEvent{Source: AlertEngine, Summary: "read-only", Detail: "busy"}, Producer: ProducerEngine, To: []string{rec.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if payload, ok := alert.Alert(); !ok || payload.Source != AlertEngine || payload.Detail != "busy" {
		t.Fatalf("alert payload = %+v, %v", payload, ok)
	}
	flush, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: EngineEvent{Minutes: 15}, FreshID: true, NeedsRoot: true, To: []string{rec.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if payload, ok := flush.Engine(); !ok || payload.Minutes != 15 {
		t.Fatalf("engine payload = %+v, %v", payload, ok)
	}
	if !strings.Contains(flush.Text, "for 15 minutes") {
		t.Fatalf("flush text = %q", flush.Text)
	}
	question, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: QuestionEvent{QuestionID: "q1", Post: "msg_0000000000000001", Reason: "session closed"}, ID: "msg_0000000000000009", To: []string{rec.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if payload, ok := question.Question(); !ok || payload.QuestionID != "q1" || payload.Post != "msg_0000000000000001" {
		t.Fatalf("question payload = %+v, %v", payload, ok)
	}
	if question.ID != "msg_0000000000000009" {
		t.Fatalf("question post ID = %q, want the caller's stable ID", question.ID)
	}
}

// Two instruction notes with the same sections but their own occurrence are
// two posts, and a retry of one occurrence keeps its post. Without the
// occurrence in the key, a change that reverts to an earlier state would reuse
// the first note and the session would keep the reverted instructions.
func TestInstructionNotesKeyOnTheirOccurrence(t *testing.T) {
	store, rec := newEventStore(t)
	sample := []InstructionChange{
		{Name: "Room rules", Action: InstructionItems, Added: []string{"r2 Check the running note."}},
		{Name: "Your running jobs", Action: InstructionRemoved, Removed: []string{"- sh_1 · Watch the build"}},
	}
	post := func(occurrence int) Message {
		t.Helper()
		msg, err := store.PostEvent(rec.ID, SessionEvent{Type: SessionInstructions, Session: "work:kid", Sections: sample, Detail: "- Room rules: added.", Occurrence: occurrence}, "work:kid")
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	first, second := post(0), post(1)
	if first.ID == "" || second.ID == "" {
		t.Fatalf("a note was dropped: %q, %q", first.ID, second.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("two occurrences share the post ID %s", first.ID)
	}
	// The same occurrence again is a retry of one change: it keeps its post.
	if again := post(1); again.ID != second.ID {
		t.Fatalf("retry of occurrence 1 posted %s after %s", again.ID, second.ID)
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("stored notes = %d, %v", len(msgs), err)
	}
	for _, msg := range msgs {
		if payload, ok := msg.Session(); !ok || len(payload.Sections) != 2 {
			t.Fatalf("note payload = %+v, %v", payload, ok)
		}
	}
}
