package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

type fixture struct {
	service *Service
	rooms   *room.Store
	rec     room.Record
	root    sessionMeta
	parent  sessionMeta
	child   sessionMeta
	sibling sessionMeta
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dataDir := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(dataDir, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "alpha", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionlife.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(dataDir); err != nil {
			t.Error(err)
		}
	})
	sessions := filepath.Join(dataDir, "sessions")
	f := &fixture{rooms: rooms, rec: rec, now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	f.root = f.session(t, sessions, "root", "", "root")
	f.parent = f.session(t, sessions, "parent", f.root.ID, "")
	f.child = f.session(t, sessions, "child", f.parent.ID, "")
	f.sibling = f.session(t, sessions, "sibling", f.parent.ID, "")
	f.service = New(rooms, sessions)
	f.service.Now = func() time.Time { return f.now }
	return f
}

func (f *fixture) session(t *testing.T, store, name, parent, role string) sessionMeta {
	t.Helper()
	meta := sessionMeta{ID: "ses_test_" + name, Name: name, Role: role, RoomID: f.rec.ID, Room: f.rec.Name, ParentSession: parent}
	if err := sessionlife.SaveContext(context.Background(), store, meta.ID, meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func (f *fixture) actor(meta sessionMeta) authority.Actor {
	role := authority.Session
	if meta.Role == "root" {
		role = authority.Root
	}
	return authority.Actor{Role: role, Session: meta.ID, Room: f.rec.ID}
}

func (f *fixture) owner() authority.Actor { return authority.Actor{Role: authority.Owner} }

func (f *fixture) create(t *testing.T, class Class) Request {
	t.Helper()
	return f.createAs(t, f.actor(f.child), class)
}

func (f *fixture) createAs(t *testing.T, actor authority.Actor, class Class) Request {
	t.Helper()
	r, err := f.service.Create(context.Background(), actor, CreateInput{Kind: Kind("fake"), Class: class, Payload: json.RawMessage(`{"operation":"write"}`), Command: "fake write", Title: "Held write", Body: "full body"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRoutingPassFromParentToRootAndOwner(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	if r.State != StatePending || r.At != "parent" {
		t.Fatalf("initial request = %+v", r)
	}
	r, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionPass})
	if err != nil || r.At != "root" {
		t.Fatalf("parent pass = %+v, %v", r, err)
	}
	r, err = f.service.Decide(context.Background(), f.actor(f.root), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionPass, Reason: "owner should see"})
	if err != nil || r.At != Owner {
		t.Fatalf("root pass = %+v, %v", r, err)
	}
	r, err = f.service.Decide(context.Background(), f.owner(), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionApprove})
	if err != nil || r.State != StateApproved {
		t.Fatalf("owner approval = %+v, %v", r, err)
	}
}

func TestDecisionGuardsAndOwnerClass(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	for name, actor := range map[string]authority.Actor{"self": f.actor(f.child), "sibling": f.actor(f.sibling), "root before its turn": f.actor(f.root)} {
		if _, err := f.service.Decide(context.Background(), actor, DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionApprove}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s decision error = %v", name, err)
		}
	}
	ownerRequest := f.createAs(t, f.actor(f.sibling), ClassOwner)
	if ownerRequest.At != Owner {
		t.Fatalf("owner-class holder = %q", ownerRequest.At)
	}
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: ownerRequest.ID, Action: ActionApprove}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent decided owner class: %v", err)
	}
	if _, err := f.service.Decide(context.Background(), f.owner(), DecisionInput{Room: f.rec.ID, RequestID: ownerRequest.ID, Action: ActionDeny}); err == nil {
		t.Fatal("deny without a reason succeeded")
	}
}

func TestRootAgentRequestIsAutoApproved(t *testing.T) {
	f := newFixture(t)
	r := f.createAs(t, f.actor(f.root), ClassAgent)
	if r.State != StateApproved || r.At != Owner {
		t.Fatalf("root request = %+v", r)
	}
	messages, err := f.rooms.Messages(f.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Notice.Kind != string(room.TypeApprovalDecided) {
		t.Fatalf("root auto events = %+v", messages)
	}
}

func TestDuplicateCancelAndExpiry(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	duplicate, err := f.service.Create(context.Background(), f.actor(f.child), CreateInput{Kind: Kind("fake"), Class: ClassAgent, Payload: json.RawMessage(`{"operation":"write"}`)})
	if err != nil || duplicate.ID != r.ID {
		t.Fatalf("duplicate = %+v, %v; want %s", duplicate, err, r.ID)
	}
	ownerClass, err := f.service.Create(context.Background(), f.actor(f.child), CreateInput{Kind: Kind("fake"), Class: ClassOwner, Payload: json.RawMessage(`{"operation":"write"}`)})
	if err != nil || ownerClass.ID == r.ID {
		t.Fatalf("owner-class request reused agent request: %+v, %v", ownerClass, err)
	}
	otherKind, err := f.service.Create(context.Background(), f.actor(f.child), CreateInput{Kind: Kind("other"), Class: ClassAgent, Payload: json.RawMessage(`{"operation":"write"}`)})
	if err != nil || otherKind.ID == r.ID {
		t.Fatalf("other kind reused fake request: %+v, %v", otherKind, err)
	}
	f.now = f.now.Add(DefaultExpiry + time.Second)
	if err := f.service.ExpireDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	expired, err := f.service.Get(f.actor(f.child), f.rec.ID, r.ID)
	if err != nil || expired.State != StateExpired {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
	f.now = f.now.Add(-DefaultExpiry)
	fresh := f.create(t, ClassAgent)
	cancelled, err := f.service.Cancel(context.Background(), f.actor(f.child), f.rec.ID, fresh.ID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
}

func TestCloseSessionMovesHolderAndCancelsRequester(t *testing.T) {
	f := newFixture(t)
	held := f.create(t, ClassAgent)
	if err := f.service.CloseSession(context.Background(), f.rec.ID, f.parent.ID, f.root.ID); err != nil {
		t.Fatal(err)
	}
	moved, err := f.service.Get(f.actor(f.child), f.rec.ID, held.ID)
	if err != nil || moved.At != "root" {
		t.Fatalf("moved holder = %+v, %v", moved, err)
	}
	if err := f.service.CloseSession(context.Background(), f.rec.ID, f.child.ID, f.root.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.service.Get(f.actor(f.child), f.rec.ID, held.ID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("closed requester = %+v, %v", cancelled, err)
	}
}

func TestWaitWakesOnFinalDecision(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	type outcome struct {
		request Request
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := f.service.Wait(context.Background(), f.actor(f.child), f.rec.ID, r.ID, time.Second)
		done <- outcome{got, err}
	}()
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionDeny, Reason: "not safe"}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.request.State != StateDenied || got.request.Result.Reason != "not safe" {
		t.Fatalf("wait = %+v, %v", got.request, got.err)
	}
}

func TestRequestNoticeNamesTheDecidingCommands(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	messages, err := f.rooms.Messages(f.rec.ID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %+v, %v", messages, err)
	}
	for _, want := range []string{"dyna request approve " + r.ID, "dyna request deny " + r.ID + " --reason \"<why>\""} {
		if !strings.Contains(messages[0].Text, want) {
			t.Fatalf("notice %q does not name %q", messages[0].Text, want)
		}
	}
}

func TestApprovalEventsWakeOnlyTheirTarget(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionPass}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Decide(context.Background(), f.actor(f.root), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionDeny, Reason: "not safe"}); err != nil {
		t.Fatal(err)
	}
	messages, err := f.rooms.Messages(f.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("events = %+v", messages)
	}
	if got := messages[0]; got.Notice.Kind != string(room.TypeApprovalRequested) || !reflect.DeepEqual(got.To, []string{f.parent.Address()}) || !got.Wake {
		t.Errorf("requested = %+v", got)
	}
	if got := messages[1]; got.Notice.Kind != string(room.TypeApprovalDecided) || !reflect.DeepEqual(got.To, []string{f.root.Address()}) || !got.Wake {
		t.Errorf("pass = %+v", got)
	}
	if got := messages[2]; got.Notice.Kind != string(room.TypeApprovalDecided) || got.Wake {
		t.Errorf("denial decision = %+v", got)
	}
	if got := messages[3]; got.Notice.Kind != string(room.TypeApprovalResult) || !reflect.DeepEqual(got.To, []string{f.child.Address()}) || !got.Wake {
		t.Errorf("result = %+v", got)
	}
}

func TestSettingsAreOwnerOnlyAndPersist(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.Settings(f.actor(f.root), SettingsInput{Room: f.rec.ID, Wait: 90 * time.Second, Expiry: 2 * time.Hour, Set: true}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("root settings error = %v", err)
	}
	settings, err := f.service.Settings(f.owner(), SettingsInput{Room: f.rec.ID, Wait: 90 * time.Second, Expiry: 2 * time.Hour, Set: true})
	if err != nil || settings.Wait != 90*time.Second || settings.Expiry != 2*time.Hour {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	rec, err := f.rooms.Resolve(f.rec.ID)
	if err != nil || rec.ApprovalWait != 90*time.Second || rec.ApprovalExpiry != 2*time.Hour {
		t.Fatalf("stored room = %+v, %v", rec, err)
	}
}

func TestApprovalListUsesDisplayHolder(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	list, err := f.service.List(f.owner(), f.rec.ID)
	if err != nil || len(list) != 1 || list[0] != (Summary{ID: r.ID, State: StatePending, At: "parent"}) {
		t.Fatalf("list = %+v, %v", list, err)
	}
}
