package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestIssueBindings(t *testing.T) {
	s := testServices(t, false)
	r, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "issues", CreatedBy: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.rooms.Notify.Rooms.FileIssue(r.ID, "Later", "detail", "", 0, r.Name)
	if err != nil {
		t.Fatal(err)
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Issues", "CloseIssue", "IssueGoal", "ExportIssue"} {
		if calls[name] == nil {
			t.Fatalf("missing %s binding", name)
		}
	}
	raw := func(v any) json.RawMessage {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	got, err := calls["Issues"](t.Context(), []json.RawMessage{raw(r.ID), raw(false)})
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got.([]room.Issue)
	if !ok || len(rows) != 1 || rows[0].ID != i.ID {
		t.Fatalf("rows: %+v", got)
	}
	if _, err = calls["CloseIssue"](t.Context(), []json.RawMessage{raw(r.ID), raw(i.ID), raw("fixed")}); err != nil {
		t.Fatal(err)
	}
	readonly, _, err := callsFor(s, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CloseIssue", "IssueGoal", "ExportIssue"} {
		if _, err = readonly[name](t.Context(), nil); err == nil {
			t.Fatalf("%s allowed in read-only server", name)
		}
	}
}

func TestCloseIssueUsesCallerPower(t *testing.T)  { testIssueCallerPower(t, "CloseIssue") }
func TestIssueGoalUsesCallerPower(t *testing.T)   { testIssueCallerPower(t, "IssueGoal") }
func TestExportIssueUsesCallerPower(t *testing.T) { testIssueCallerPower(t, "ExportIssue") }

func testIssueCallerPower(t *testing.T, method string) {
	t.Helper()
	storetest.Isolate(t)
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "issues-power"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.rooms.Notify.Rooms.UpdatePreferences(rec.ID, func(r *room.Record) error { r.GitHubAccess = ghclient.ReadWrite; return nil }); err != nil {
		t.Fatal(err)
	}
	other, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "other-power"})
	if err != nil {
		t.Fatal(err)
	}
	root := subagent.Meta{ID: "ses_20261020-000000-abcde111", Name: "root", Room: rec.Name, RoomID: rec.ID, Role: subagent.RoleRoot, Turns: 1}
	foreignRoot := subagent.Meta{ID: "ses_20261020-000000-abcde333", Name: "root", Room: other.Name, RoomID: other.ID, Role: subagent.RoleRoot}
	for _, meta := range []subagent.Meta{root, foreignRoot} {
		if err := sessionlife.Save(s.rooms.Notify.Sessions.Store.Root, meta.ID, meta); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(filepath.Dir(s.rooms.Notify.Sessions.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES (?,1,1,?)", root.ID, []byte(`{"n":1,"status":"running"}`)); err != nil {
		t.Fatal(err)
	}
	repairs := 0
	s.rooms.Notify.Sessions.Store.ReportRepaired = func(*subagent.Store, string, int) { repairs++ }
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	owner := authority.Actor{Role: authority.Owner}
	session := authority.Actor{Role: authority.Session, Session: "ses_20261020-000000-abcde222", Room: rec.ID}
	for _, tc := range []struct {
		name          string
		caller        websvc.Caller
		actor         authority.Actor
		claim         callspec.Claim
		remote, allow bool
		by            string
	}{
		{name: "session", caller: websvc.Caller{Kind: websvc.Session, Session: session.Session}, actor: session},
		{name: "worker", caller: websvc.Caller{Kind: websvc.Worker, Run: "wf_test", Agent: 1}, actor: authority.Actor{Role: authority.Worker, Run: "wf_test", Agent: 1, Room: rec.ID}},
		{name: "root in another room", caller: websvc.Caller{Kind: websvc.Session, Session: foreignRoot.ID}, actor: authority.Actor{Role: authority.Root, Session: foreignRoot.ID, Room: other.ID}},
		{name: "report-only session claim", caller: websvc.Caller{Kind: websvc.Unverified}, actor: owner, claim: callspec.Claim{Session: session.Session, Room: rec.ID}},
		{name: "root", caller: websvc.Caller{Kind: websvc.Session, Session: root.ID}, actor: authority.Actor{Role: authority.Root, Session: root.ID, Room: rec.ID}, allow: true, by: rec.Name + ":root"},
		{name: "owner", caller: websvc.Caller{Kind: websvc.Owner}, actor: owner, allow: true, by: rec.Name},
		{name: "paired owner", remote: true, actor: owner, allow: true, by: rec.Name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue, err := s.rooms.Notify.Rooms.FileIssue(rec.ID, "Caller guard", "detail", "", 0, rec.Name)
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.rooms.Goals(rec.ID, "", 50)
			if err != nil {
				t.Fatal(err)
			}
			ctx := authority.WithActor(t.Context(), tc.actor)
			if !tc.remote {
				ctx = localCallerContext(t, tc.caller, tc.actor)
			}
			ctx = callspec.WithClaim(ctx, tc.claim)
			value := "fixed"
			if method == "IssueGoal" {
				value = ""
			}
			raw := func(v string) json.RawMessage { b, _ := json.Marshal(v); return b }
			args := []json.RawMessage{raw(rec.ID), raw(issue.ID)}
			if method != "ExportIssue" {
				args = append(args, raw(value))
			}
			_, err = calls[method](ctx, args)
			repositoryCheck := method == "ExportIssue" && tc.allow
			if repositoryCheck {
				if err == nil || !strings.Contains(err.Error(), "no repository") {
					t.Fatalf("allowed caller did not reach repository check: %v", err)
				}
			} else if (err == nil) != tc.allow {
				t.Fatalf("allowed = %v, want %v: %v", err == nil, tc.allow, err)
			}
			if !tc.allow && !strings.HasPrefix(err.Error(), "ChangeIssue is ") && !strings.Contains(err.Error(), "only in its own room") && !(tc.caller.Kind == websvc.Unverified && strings.Contains(err.Error(), "could not verify")) {
				t.Fatalf("wrong refusal: %v", err)
			}
			rows, err := s.rooms.Notify.Rooms.Issues(rec.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			var saved room.Issue
			for _, row := range rows {
				if row.ID == issue.ID {
					saved = row
				}
			}
			if !tc.allow || repositoryCheck {
				if saved.State != "open" || saved.GoalID != "" || saved.ChangedBy != "" {
					t.Fatalf("refused call changed issue: %+v", saved)
				}
				after, err := s.rooms.Goals(rec.ID, "", 50)
				if err != nil || len(after.Items) != len(before.Items) {
					t.Fatalf("refused call created a goal: %+v, %v", after, err)
				}
				return
			}
			wantState := "closed"
			if method == "IssueGoal" {
				wantState = "goal"
			}
			if saved.State != wantState || saved.ChangedBy != tc.by {
				t.Fatalf("saved issue: %+v", saved)
			}
			if method == "IssueGoal" {
				goal, err := s.rooms.Goal(rec.ID, saved.GoalID)
				setter := "owner"
				if tc.actor.Role == authority.Root {
					setter = root.ID
				}
				if err != nil || goal.Setter != setter {
					t.Fatalf("goal setter: %+v, %v", goal, err)
				}
			}
		})
	}
	if repairs != 0 {
		t.Fatalf("identity lookup repaired %d turns", repairs)
	}
	turn, err := s.rooms.Notify.Sessions.Store.ReadTurn(root.ID, 1)
	if err != nil || turn.Status != subagent.TurnRunning {
		t.Fatalf("caller turn changed: %+v, %v", turn, err)
	}

}
