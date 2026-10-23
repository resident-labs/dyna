package roomsvc

import (
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestOwnerRulesUseRoleAuthorityAndRecordedAuditIdentity(t *testing.T) {
	const sessionID = "ses_20261022-120000-1234abcd"
	for _, tc := range []struct {
		name     string
		role     authority.Role
		recorded string
		allowed  bool
	}{
		{"worker", authority.Worker, "run wf_1 agent 2", false},
		{"owner", authority.Owner, "owner audit", true},
		{"root", authority.Root, "root audit", true},
		{"session recorded as owner", authority.Session, "owner", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			r := e.room("work")
			actor := authority.Actor{Role: tc.role, Run: "wf_1", Agent: 2}
			if tc.role == authority.Root || tc.role == authority.Session {
				actor.Session = sessionID
				recordtest.Write(t, e.mgr.Store.Root, sessionID, subagent.Meta{ID: sessionID, Name: "helper", RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true})
			}
			ctx := WithGoalAuthor(authority.WithActor(t.Context(), actor), tc.recorded)
			store := e.svc.goalService().Store
			ownerRule, err := store.AddRule(r.ID, "owner", "owner", "Original owner rule")
			if err != nil {
				t.Fatal(err)
			}
			before, err := e.svc.Rules(r.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := e.svc.rooms().Messages(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			edited, editErr := e.svc.EditRule(ctx, r.ID, false, ownerRule.ID, "Changed owner rule")
			_, removeErr := e.svc.RemoveRule(ctx, r.ID, false, ownerRule.ID)
			after, err := e.svc.Rules(r.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			notices, err := e.svc.rooms().Messages(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.allowed {
				if editErr != nil || removeErr != nil || len(edited.Edits) != 1 || edited.Edits[0].By != tc.recorded || len(after.Rules) != 0 {
					t.Fatalf("authorized change: edit=%+v, %v; removal=%v; rules=%+v", edited, editErr, removeErr, after)
				}
				if len(notices) != len(messages)+2 {
					t.Fatalf("success notices = %+v", notices)
				}
				for _, notice := range notices[len(messages):] {
					ev, ok := notice.Rule()
					if !ok || ev.Actor != tc.recorded {
						t.Fatalf("notice audit identity = %q, want %q", ev.Actor, tc.recorded)
					}
				}
			} else {
				if editErr == nil || removeErr == nil || after.Hash != before.Hash || len(after.Rules) != 1 || after.Rules[0].Text != ownerRule.Text || len(notices) != len(messages) {
					t.Fatalf("owner rule refusal: edit=%v removal=%v rules=%+v notices=%+v", editErr, removeErr, after, notices)
				}
			}
			if tc.role == authority.Worker {
				agentRule, err := store.AddRule(r.ID, "agent", "agent", "Agent rule")
				if err != nil {
					t.Fatal(err)
				}
				edited, err = e.svc.EditRule(ctx, r.ID, false, agentRule.ID, "Changed agent rule")
				if err != nil || len(edited.Edits) != 1 || edited.Edits[0].By != tc.recorded {
					t.Fatalf("worker agent edit: %+v, %v", edited, err)
				}
				if _, err := e.svc.RemoveRule(ctx, r.ID, false, agentRule.ID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
