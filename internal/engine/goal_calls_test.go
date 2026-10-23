package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestGoalWritesRecordCaller(t *testing.T) {
	id := "ses_20261020-000001-aaaaaaaa"
	for _, tc := range []struct {
		name    string
		caller  websvc.Caller
		local   bool
		actor   authority.Actor
		claim   callspec.Claim
		enforce bool
		want    string
		logged  bool
	}{
		{"owner", websvc.Caller{Kind: websvc.Owner}, true, authority.Actor{Role: authority.Owner}, callspec.Claim{}, false, "owner", false},
		{"verified owner ignores claim", websvc.Caller{Kind: websvc.Owner}, true, authority.Actor{Role: authority.Owner}, callspec.Claim{Session: "ses_missing", Room: "other"}, false, "owner", false},
		{"paired device", websvc.Caller{}, false, authority.Actor{Role: authority.Owner}, callspec.Claim{}, false, "owner", false},
		{"session", websvc.Caller{Kind: websvc.Session, Session: id}, true, authority.Actor{Role: authority.Session, Session: id}, callspec.Claim{}, false, id, false},
		{"root", websvc.Caller{Kind: websvc.Session, Session: id}, true, authority.Actor{Role: authority.Root, Session: id}, callspec.Claim{}, false, id, false},
		{"worker", websvc.Caller{Kind: websvc.Worker, Run: "wf_1"}, true, authority.Actor{Role: authority.Worker, Run: "wf_1", Agent: 2}, callspec.Claim{Session: "ses_missing", Room: "other"}, false, "run wf_1 agent 2", false},
		{"report-only unverified", websvc.Caller{Kind: websvc.Unverified}, true, authority.Actor{Role: authority.Owner}, callspec.Claim{}, false, "unverified", true},
		{"report-only mismatched session", websvc.Caller{Kind: websvc.Session, Session: id}, true, authority.Actor{Role: authority.Owner}, callspec.Claim{}, false, "unverified", true},
		{"report-only claimed session", websvc.Caller{Kind: websvc.Unverified}, true, authority.Actor{Role: authority.Owner}, callspec.Claim{Session: id, Room: "work"}, false, "unverified", true},
		{"enforced claimed session", websvc.Caller{Kind: websvc.Unverified}, true, authority.Actor{Role: authority.Owner}, callspec.Claim{Session: id}, true, "error", false},
		{"enforced unverified", websvc.Caller{Kind: websvc.Unverified}, true, authority.Actor{}, callspec.Claim{}, true, "error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs []string
			actor, by, err := mutationActor(tc.caller, tc.local, tc.actor, tc.claim, tc.enforce, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }, "changing goals or rules")
			logged := len(logs) > 0
			if tc.want == "error" {
				if err == nil {
					t.Fatal("unverified caller accepted with enforcement")
				}
				return
			}
			if err != nil || by != tc.want || logged != tc.logged {
				t.Fatalf("caller = %+v, %q, %v, logged %v", actor, by, err, logged)
			}
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			svc := roomsvc.New(notify.New(nil, nil))
			rec, err := svc.Notify.Rooms.Create(room.CreateInput{Name: "work"})
			if err != nil {
				t.Fatal(err)
			}
			if actor.Session != "" {
				recordtest.Write(t, svc.Notify.Sessions.Store.Root, id, subagent.Meta{ID: id, Name: "helper", RoomID: rec.ID, Room: rec.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture"})
			}
			ctx := roomsvc.WithGoalAuthor(authority.WithActor(t.Context(), actor), by)
			g, err := svc.AddGoal(ctx, rec.ID, "Record the caller", nil)
			if err != nil || g.Setter != tc.want {
				t.Fatalf("goal = %+v, %v", g, err)
			}
			rule, err := svc.AddRule(ctx, rec.ID, false, "Record the caller")
			if err != nil || rule.Setter != tc.want {
				t.Fatalf("rule = %+v, %v", rule, err)
			}
			ownerCtx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
			ownerRule, err := svc.AddRule(ownerCtx, rec.ID, false, "Owner rule")
			if err != nil {
				t.Fatal(err)
			}
			target := ownerRule.ID
			if actor.Role == authority.Session || actor.Role == authority.Worker {
				if _, err := svc.EditRule(ctx, rec.ID, false, ownerRule.ID, "Forbidden"); err == nil {
					t.Fatal("claimed session edited an owner rule")
				}
				if _, err := svc.RemoveRule(ctx, rec.ID, false, ownerRule.ID); err == nil {
					t.Fatal("claimed session removed an owner rule")
				}
				if tc.want == "unverified" && !strings.Contains(strings.Join(logs, "\n"), "recorded as unverified") {
					t.Fatalf("refusal audit log = %v", logs)
				}
				if actor.Role == authority.Session && !g.HeldBy(id) {
					t.Fatalf("holder lookup ignored authorization session: %+v", g)
				}
				target = rule.ID
			}
			edited, err := svc.EditRule(ctx, rec.ID, false, target, "Changed with recorded caller")
			if err != nil || len(edited.Edits) != 1 || edited.Edits[0].By != tc.want {
				t.Fatalf("rule edit = %+v, %v", edited, err)
			}
			if _, err := svc.RemoveRule(ctx, rec.ID, false, target); err != nil {
				t.Fatal(err)
			}
			messages, err := svc.Notify.Rooms.Messages(rec.ID)
			if err != nil || len(messages) == 0 {
				t.Fatal(messages, err)
			}
			who := tc.want
			if tc.want == id {
				who = rec.Name + ":helper"
			}
			if messages[len(messages)-1].From != who {
				t.Fatalf("removal notice = %+v", messages[len(messages)-1])
			}
			saved, err := svc.Goal(rec.ID, g.ID)
			if err != nil || saved.Setter != tc.want {
				t.Fatal(saved, err)
			}
		})
	}
}

func TestNewGoalMutationsStayLocal(t *testing.T) {
	calls := testCalls(t, false)
	for _, name := range []string{"AddGoal", "EditGoalSelectors"} {
		if _, err := calls[name](t.Context(), nil); err == nil || err.Error() != name+" is available only through the local engine socket" {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestUnverifiedGoalCallerKeepsSessionAuthority(t *testing.T) {
	session := authority.Actor{Role: authority.Session, Session: "ses_20261020-000001-aaaaaaaa", Room: "room_work"}
	got, by, err := mutationActor(websvc.Caller{Kind: websvc.Unverified}, true, session, callspec.Claim{}, false, func(string, ...any) {}, "changing goals or rules")
	if err != nil || got != session || by != "unverified" {
		t.Fatalf("caller = %+v, %q, %v", got, by, err)
	}
}

// Every call that the caller policy wraps is a bound call, so a renamed or
// misspelled name cannot leave a session or goal mutation unwrapped.
func TestProtectedCallsAreBound(t *testing.T) {
	bound := map[string]bool{}
	for _, b := range bindings(testServices(t, false)) {
		bound[b.Name] = true
	}
	for _, name := range sessionCalls {
		if !bound[name] {
			t.Errorf("session call %s is not bound", name)
		}
	}
	for _, name := range prCalls {
		if !bound[name] {
			t.Errorf("PR call %s is not bound", name)
		}
	}
	for _, name := range issueCalls {
		if !bound[name] {
			t.Errorf("issue call %s is not bound", name)
		}
	}
}

// CloseForEngine and StartForEngine check no caller, so no engine call may
// reach them.
func TestEngineMaintenanceIsNeverACall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"CloseForEngine", "StartForEngine"} {
			if strings.Contains(string(src), method) {
				t.Errorf("%s uses %s; engine calls must use the checked method", file, method)
			}
		}
	}
}
