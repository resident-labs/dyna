package notify

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestServiceSessionsGroupMentions(t *testing.T) {
	for _, group := range []string{"team", "siblings", "children"} {
		t.Run(group, func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			lead := e.start(rec, "lead", nil)
			var parent *subagent.Info
			sender := Sender{Kind: SenderOwner, Room: rec.Name}
			if group == "siblings" {
				sender = Sender{Kind: SenderSession, Room: rec.Name, Session: lead}
			}
			if group == "children" {
				parent = &lead
				sender = Sender{Kind: SenderSession, Room: rec.Name, Session: lead}
			}
			normal := e.start(rec, "worker", parent)
			// Service names are reserved for their role, so these sessions
			// take another name and become services through their metadata.
			var services []string
			for _, role := range []string{"reviewer", "keeper"} {
				info := e.start(rec, role+"-service", parent)
				if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Role = role }); err != nil {
					t.Fatal(err)
				}
				services = append(services, info.Address())
			}
			got, err := e.svc.resolve(sender, []string{"@" + group})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{normal.Address()}
			if group == "team" {
				want = append(want, lead.Address())
			}
			if group == "children" {
				want = append(want, services...)
			}
			if !reflect.DeepEqual(sorted(got.addrs()), sorted(want)) {
				t.Fatalf("recipients = %v; want %v", got.addrs(), want)
			}
		})
	}
}

func TestServiceSessionDirectMentions(t *testing.T) {
	for _, role := range []string{"reviewer", "keeper"} {
		t.Run(role, func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			worker := e.start(rec, "worker", nil)
			info, _, err := e.mgr.Start(context.Background(), subagent.StartOptions{Goal: "Finish the test task", Role: role, Name: role, Room: rec.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: first"})
			if err != nil {
				t.Fatal(err)
			}
			info = e.wait(info.ID, 1)
			if !info.Service {
				t.Fatal("managed session lacks service flag")
			}
			if _, err := e.svc.CompleteSessionTurn(e.ctx, info.ID, 1); err != nil {
				t.Fatal(err)
			}
			messages, err := e.svc.Rooms.Messages(rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			reported := false
			for _, message := range messages {
				if message.From == info.Address() && message.Kind == "completed" && reflect.DeepEqual(message.To, []string{rec.Name}) {
					reported = true
				}
			}
			if reported {
				t.Fatal("service completion reached room owner")
			}
			result, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: worker}, Mentions: []string{"@" + role}, Text: "SLEEP:0\nRESPOND: direct"})
			if err != nil || len(result.Deliveries) != 1 {
				t.Fatalf("direct post: %+v %v", result, err)
			}
			if role == subagent.RoleKeeper {
				// The keeper reads posts with its next assignment; they never wake it.
				if _, err := e.mgr.TickSessions(e.ctx); err != nil {
					t.Fatal(err)
				}
				if meta, _ := e.mgr.Store.ReadMeta(info.ID); meta.Turns != 1 {
					t.Fatalf("a direct post woke the keeper: %d turns", meta.Turns)
				}
				return
			}
			e.wait(info.ID, 2)
		})
	}
}

func TestServiceCrewReceivesOnlyParentGroupAssignments(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	reviewer := e.start(rec, "reviewer", nil)
	meta, err := e.mgr.Store.UpdateMeta(reviewer.ID, func(m *subagent.Meta) { m.Service = true })
	if err != nil {
		t.Fatal(err)
	}
	reviewer.Meta = meta
	first := e.start(rec, "first", &reviewer)
	second := e.start(rec, "second", &reviewer)
	if !first.Service || !second.Service {
		t.Fatal("helpers did not inherit service flag")
	}
	got, err := e.svc.resolve(Sender{Kind: SenderSession, Room: rec.Name, Session: reviewer}, []string{"@children"})
	if err != nil || !reflect.DeepEqual(sorted(got.addrs()), sorted([]string{first.Address(), second.Address()})) {
		t.Fatalf("parent assignment: %+v %v", got, err)
	}
	if _, err := e.svc.resolve(Sender{Kind: SenderSession, Room: rec.Name, Session: first}, []string{"@siblings"}); err == nil {
		t.Fatal("sibling broadcast reached service helper")
	}
	frame := e.mgr.Store.SelectorContext(first.Meta)
	if !strings.Contains(frame, "@siblings = none") {
		t.Fatalf("service sibling in frame: %s", frame)
	}
	frame = e.mgr.Store.SelectorContext(reviewer.Meta)
	if !strings.Contains(frame, first.Address()) || !strings.Contains(frame, second.Address()) {
		t.Fatalf("crew missing in parent frame: %s", frame)
	}
}
