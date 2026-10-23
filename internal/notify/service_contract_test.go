package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func markService(t *testing.T, e *testEnv, info subagent.Info, role string) subagent.Info {
	t.Helper()
	meta, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Service, m.Role = true, role })
	if err != nil {
		t.Fatal(err)
	}
	info.Meta = meta
	return info
}

func TestServiceContractQuietSuccessAndFailedAlert(t *testing.T) {
	for _, role := range []string{"reviewer", "keeper"} {
		t.Run(role, func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			// Service names are reserved for their role; markService sets it.
			info := markService(t, e, e.start(rec, role+"-service", nil), role)
			_, err := e.mgr.Store.UpdateTurn(info.ID, 1, func(turn *subagent.Turn) { turn.Result = map[string]any{"goals": []any{}} })
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, info.ID, 1)
			if err != nil || res.Message.ID != "" {
				t.Fatalf("service final escaped: %+v %v", res, err)
			}
			_, err = e.mgr.Store.UpdateTurn(info.ID, 1, func(turn *subagent.Turn) {
				turn.Reported = false
				turn.Status, turn.Error = subagent.TurnError, "provider failed"
			})
			if err != nil {
				t.Fatal(err)
			}
			res, err = e.svc.CompleteSessionTurn(e.ctx, info.ID, 1)
			if err != nil || res.Message.Kind != string(room.FamilyService) || res.Message.Notice.Kind != string(room.TypeFailed) || !reflect.DeepEqual(res.Message.To, []string{rec.Name}) {
				t.Fatalf("failure alert: %+v %v", res, err)
			}
			// The engine posts the typed event: the registry sets its sender,
			// kind, text and wake rule.
			payload, ok := res.Message.ServiceFailed()
			if !ok || payload.Service != serviceRole(role) || payload.Session != info.Address() || payload.Error != "provider failed" {
				t.Fatalf("failure payload = %+v, %v", payload, ok)
			}
			// One alert from the service, never a child-failure report with a
			// transfer hint: a service changes through `dyna services`.
			text := res.Message.Text
			if !strings.HasPrefix(text, "The ") || !strings.Contains(text, " service failed on turn 1") || !strings.Contains(text, "Session: "+info.Address()) {
				t.Fatalf("alert text = %q", text)
			}
			if strings.Contains(text, "dyna subagent transfer") || strings.Contains(text, "dyna subagent output") {
				t.Fatalf("alert keeps child-failure advice: %q", text)
			}
		})
	}
}

func TestServiceContractDirectReplyTargetsOnlySender(t *testing.T) {
	for _, fromOwner := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner", false: "session"}[fromOwner], func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			worker := e.start(rec, "worker", nil)
			service := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
			sender := Sender{Kind: SenderOwner, Room: rec.Name}
			target := "@root"
			if !fromOwner {
				sender = Sender{Kind: SenderSession, Room: rec.Name, Session: worker}
				target = "@worker"
			}
			original, err := e.svc.Post(e.ctx, PostInput{Sender: sender, Mentions: []string{"@reviewer"}, Text: "SLEEP:0\nRESPOND: question"})
			if err != nil {
				t.Fatal(err)
			}
			e.wait(service.ID, 2)
			input := PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: service}, Mentions: []string{target}, ReplyTo: original.Message.ID, Text: "SLEEP:0\nRESPOND: reply"}
			res, err := e.svc.Post(e.ctx, input)
			if err != nil || !reflect.DeepEqual(res.Message.To, []string{original.Message.From}) {
				t.Fatalf("direct reply: %+v %v", res, err)
			}
			if !fromOwner {
				e.wait(worker.ID, 2)
			}
			input.Mentions = []string{target, "@root", "@worker"}
			if _, err := e.svc.Post(e.ctx, input); !errors.Is(err, subagent.ErrServiceOutput) {
				t.Fatalf("reply leaked to another recipient: %v", err)
			}
		})
	}
}

func TestServiceContractRejectsBroadcastAndAskWithoutNewTurns(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	worker := e.start(rec, "worker", nil)
	service := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
	sender := Sender{Kind: SenderSession, Room: rec.Name, Session: service}
	for _, mentions := range [][]string{{"@root"}, {"@siblings"}, {"@worker"}} {
		if _, err := e.svc.Post(e.ctx, PostInput{Sender: sender, Mentions: mentions, Text: "Status", Files: []File{{Name: "status.txt", Data: []byte("status")}}}); !errors.Is(err, subagent.ErrServiceOutput) {
			t.Fatalf("broadcast allowed: %v", err)
		}
	}
	files, err := os.ReadDir(filepath.Join(e.svc.Rooms.Root, rec.ID, "files"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(files) > 0 {
		t.Fatal("rejected post copied attachments")
	}
	if _, err := e.svc.Ask(e.ctx, sender, rec.Name, "@root", "Question?", "Wait", nil, nil); !errors.Is(err, subagent.ErrServiceOutput) {
		t.Fatalf("service ask: %v", err)
	}
	posts, err := e.svc.Rooms.Messages(rec.ID)
	if err != nil || len(posts) != 0 {
		t.Fatalf("rejected output stored posts: %+v %v", posts, err)
	}
	for _, info := range []subagent.Info{worker, service} {
		got, err := e.mgr.Store.Info(info.ID)
		if err != nil || got.Turns != 1 || got.Status != subagent.StatusIdle {
			t.Fatalf("rejected output started work: %+v %v", got, err)
		}
	}
	res, err := e.svc.Post(e.ctx, PostInput{Sender: sender, Mentions: []string{"@root"}, Kind: ServiceAlertKind, Text: "Goal blocked; owner action is needed."})
	if err != nil || res.Message.Kind != ServiceAlertKind {
		t.Fatalf("domain alert rejected: %+v %v", res, err)
	}
	again, err := e.svc.Post(e.ctx, PostInput{Sender: sender, Mentions: []string{"@root"}, Kind: ServiceAlertKind, Text: "Goal blocked; owner action is needed."})
	if err != nil || again.Message.ID != res.Message.ID || len(again.Deliveries) != 0 {
		t.Fatalf("alert not deduplicated: %+v %v", again, err)
	}
}

func TestServiceContractHelperUsesCurrentParentAndCurrentTurn(t *testing.T) {
	for _, change := range []string{"stale-turn", "new-parent", "closed-parent"} {
		t.Run(change, func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
			helper := e.start(rec, "helper", &parent)
			if _, err := e.mgr.Store.UpdateTurn(helper.ID, 1, func(turn *subagent.Turn) { turn.Result = nil }); err != nil {
				t.Fatal(err)
			}
			target := parent
			if change == "new-parent" {
				target = markService(t, e, e.start(rec, "other-reviewer", nil), "reviewer")
				if _, err := e.mgr.Store.UpdateMeta(helper.ID, func(m *subagent.Meta) { m.ParentSession = target.ID }); err != nil {
					t.Fatal(err)
				}
			}
			if change == "closed-parent" {
				if _, err := e.mgr.Store.UpdateMeta(parent.ID, func(m *subagent.Meta) { m.Closed = time.Now() }); err != nil {
					t.Fatal(err)
				}
			}
			_, err := e.svc.Rooms.Post(rec.ID, room.PostInput{From: helper.Address(), To: []string{parent.Address()}, SessionIDs: map[string]string{parent.Address(): parent.ID}, Turn: 2, Text: "Old findings"})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			want := target.Address()
			if change == "closed-parent" {
				want = rec.Name
			}
			if err != nil || res.Message.Kind != ServiceAlertKind || !reflect.DeepEqual(res.Message.To, []string{want}) {
				t.Fatalf("artifact alert: %+v %v", res, err)
			}
			if change != "closed-parent" {
				e.wait(target.ID, 2)
			}
		})
	}
}

func TestServiceContractGroupPostDoesNotGrantReplyRights(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
	helper := e.start(rec, "helper", &parent)
	res, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: parent}, Mentions: []string{"@children"}, Text: "SLEEP:0\nRESPOND: assigned"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(helper.ID, 2)
	_, err = e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: helper}, Mentions: []string{"@parent"}, ReplyTo: res.Message.ID, Text: "reply"})
	if !errors.Is(err, subagent.ErrServiceOutput) {
		t.Fatalf("group granted reply rights: %v", err)
	}
}

func TestServiceContractHelperArtifactAndMissingAlert(t *testing.T) {
	for _, findings := range []bool{false, true} {
		t.Run(map[bool]string{true: "findings", false: "missing"}[findings], func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
			helper := e.start(rec, "helper", &parent)
			if !findings {
				if _, err := e.mgr.Store.UpdateTurn(helper.ID, 1, func(turn *subagent.Turn) { turn.Result = nil }); err != nil {
					t.Fatal(err)
				}
			}
			if findings {
				helper.CurrentTurn = 1
				_, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: helper}, Mentions: []string{"@parent"}, Text: "SLEEP:0\nRESPOND: Findings: no issues."})
				if err != nil {
					t.Fatal(err)
				}
				e.wait(parent.ID, 2)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if findings && res.Message.ID != "" {
				t.Fatalf("findings duplicated: %+v", res)
			}
			if !findings {
				if res.Message.Kind != ServiceAlertKind || !reflect.DeepEqual(res.Message.To, []string{parent.Address()}) || !strings.Contains(res.Message.Text, "no artifact") {
					t.Fatalf("missing findings: %+v", res)
				}
				e.wait(parent.ID, 2)
			}
			again, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			if err != nil || again.Message.ID != "" {
				t.Fatalf("duplicate completion: %+v %v", again, err)
			}
		})
	}
}

func TestServiceContractReplyFollowsRenamedSender(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	worker := e.start(rec, "worker", nil)
	service := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
	original, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: worker}, Mentions: []string{"@reviewer"}, Text: "SLEEP:0\nRESPOND: question"})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := e.mgr.Rename(context.Background(), worker.ID, "worker-new")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: service}, Mentions: []string{"@worker-new"}, ReplyTo: original.Message.ID, Text: "SLEEP:0\nRESPOND: reply"})
	if err != nil || !reflect.DeepEqual(res.Message.To, []string{renamed.Address()}) {
		t.Fatalf("reply to renamed sender: %+v %v", res, err)
	}
	other := e.start(rec, "worker", nil)
	if _, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: service}, Mentions: []string{"@" + other.Name}, ReplyTo: original.Message.ID, Text: "reply"}); !errors.Is(err, subagent.ErrServiceOutput) {
		t.Fatalf("reply reached the new holder of the old name: %v", err)
	}
}
