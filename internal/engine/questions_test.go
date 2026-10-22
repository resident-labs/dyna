package engine

import (
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

// Browser and paired-device calls have an owner actor without a local
// socket caller. ScopeOperate still cannot withdraw through either path.
func TestRemoteOwnerCannotCreateOrWithdrawQuestion(t *testing.T) {
	calls := testCalls(t, false)
	for name, args := range map[string][]json.RawMessage{
		"AskQuestion":      {json.RawMessage(`{}`)},
		"WithdrawQuestion": {json.RawMessage(`"room"`), json.RawMessage(`"q1"`), json.RawMessage(`"done"`)},
	} {
		_, err := calls[name](authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), args)
		if err == nil || name == "AskQuestion" && !strings.Contains(err.Error(), "root") || name == "WithdrawQuestion" && !strings.Contains(err.Error(), "local engine socket") {
			t.Fatalf("%s from browser = %v", name, err)
		}
	}
}

func TestQuestionEngineUsesVerifiedCaller(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "question-engine"})
	if err != nil {
		t.Fatal(err)
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &socketCallers{sessions: s.rooms.Notify.Sessions.Store}
	resolver.actForCallers(calls)
	enginetest.Serve(t, store.DataDir(), calls)
	// The socket caller is the owner. Forged process stamps must not turn
	// the sender into a session or read its nonexistent record.
	t.Setenv(mode.EnvMode, string(mode.Subagent))
	t.Setenv(mode.EnvSession, "ses_20261020-120000-deadbeef")
	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	var q questions.Question
	req := notify.AskRequest{Room: rec.ID, Text: "Proceed?", Meanwhile: "check data"}
	if err := client.Call(t.Context(), "AskQuestion", []any{req}, &q); err != nil {
		t.Fatal(err)
	}
	if q.Asker != "owner" || q.Human || q.Post == "" {
		t.Fatalf("asked question = %+v", q)
	}
	if err := client.Call(t.Context(), "AnswerQuestion", []any{rec.ID, q.ID, "", "proceed"}, nil); err != nil {
		t.Fatal(err)
	}
	var second questions.Question
	if err := client.Call(t.Context(), "AskQuestion", []any{req}, &second); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(t.Context(), "WithdrawQuestion", []any{rec.ID, second.ID, "finished"}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.rooms.Notify.QuestionStore().List(rec.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("question rows = %+v, %v", rows, err)
	}
	for _, row := range rows {
		switch row.ID {
		case q.ID:
			if row.State != "answered" || row.Answer == nil || row.Answer.By != "owner" || row.Answer.Human {
				t.Fatalf("answered = %+v", row)
			}
		case second.ID:
			if row.State != "withdrawn" || row.Withdrawn == nil || row.Withdrawn.By != "owner" {
				t.Fatalf("withdrawn = %+v", row)
			}
		}
	}
}

func TestQuestionBrowserAnswerIsHuman(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "browser-question"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.rooms.Notify.QuestionStore().Ask(questions.Question{Room: rec.ID, Asker: "owner", Text: "Proceed?", Meanwhile: "check data"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err := s.answerQuestion(ctx, rec.ID, q.ID, "", "proceed"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.rooms.Notify.QuestionStore().List(rec.ID)
	if err != nil || len(rows) != 1 || rows[0].Answer == nil || !rows[0].Answer.Human || rows[0].Answer.By != "owner" {
		t.Fatalf("browser answer = %+v, %v", rows, err)
	}
}

func TestQuestionRecordsKeepUnverifiedCaller(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		var logs []string
		snd, err := questionRecordSender(websvc.Caller{Kind: websvc.Unverified}, true, authority.Actor{Role: authority.Owner}, enforce,
			func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
			func(notify.Claim) (notify.Sender, error) {
				t.Fatal("unverified caller resolved as owner")
				return notify.Sender{}, nil
			})
		if enforce {
			if err == nil {
				t.Fatal("enforced call accepted an unverified caller")
			}
		} else if err != nil || snd.Kind != notify.SenderUnverified || snd.Human || len(logs) != 1 || !strings.Contains(logs[0], "recorded as unverified") {
			t.Fatalf("report-only sender = %+v, err=%v, logs=%v", snd, err, logs)
		}
	}
}

func TestQuestionAskRejectsUnverifiedClaim(t *testing.T) {
	req := notify.AskRequest{Claim: notify.Claim{Session: "ses_claimed", Room: "room"}}
	logged := false
	snd, err := postSender(websvc.Caller{Kind: websvc.Unverified}, true, authority.Actor{Role: authority.Owner}, req.Claim, false,
		func(string, ...any) { logged = true },
		func(claim notify.Claim) (notify.Sender, error) {
			info := subagent.Info{}
			info.ID = claim.Session
			return notify.Sender{Kind: notify.SenderSession, Session: info, Room: claim.Room}, nil
		})
	if err == nil || logged {
		t.Fatalf("unverified ask = %+v, %v, logged=%v", snd, err, logged)
	}
}
