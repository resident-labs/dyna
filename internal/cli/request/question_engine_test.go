package request

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestQuestionCLIUsesEngine(t *testing.T) {
	storetest.DataDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	storetest.ClearStamp(t)
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "question-cli"})
	if err != nil {
		t.Fatal(err)
	}
	callsSeen := []string{}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "AskQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, req notify.AskRequest) (questions.Question, error) {
			callsSeen = append(callsSeen, "ask")
			if req.Room != rec.ID || req.Text != "Proceed?" || req.Meanwhile != "check data" {
				return questions.Question{}, fmt.Errorf("unexpected request: %+v", req)
			}
			return questions.Question{ID: "q42", Post: "msg_test"}, nil
		}},
		{Name: "AnswerQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, ref, id, choice, text string) error {
			callsSeen = append(callsSeen, "answer")
			if ref != rec.ID || id != "q42" || choice != "yes" || text != "" {
				return fmt.Errorf("unexpected answer arguments")
			}
			return nil
		}},
		{Name: "WithdrawRequest", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, ref, id, reason string) error {
			callsSeen = append(callsSeen, "withdraw")
			if ref != rec.ID || id != "q42" || reason != "finished" {
				return fmt.Errorf("unexpected withdraw arguments")
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	for _, args := range [][]string{
		{"--room", rec.ID, "question", "Proceed?", "--meanwhile", "check data"},
		{"--room", rec.ID, "answer", "q42", "--choice", "yes"},
		{"--room", rec.ID, "withdraw", "q42", "--reason", "finished"},
	} {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("%v: %s: %v", args, out.String(), err)
		}
		if args[3] == "Proceed?" && !strings.Contains(out.String(), "Asked q42") {
			t.Fatal(out.String())
		}
	}
	if strings.Join(callsSeen, ",") != "ask,answer,withdraw" {
		t.Fatalf("engine calls = %v", callsSeen)
	}
	qs, err := notify.Default().QuestionStore().List(rec.ID)
	if err != nil || len(qs) != 0 {
		t.Fatalf("CLI wrote local questions: %+v, %v", qs, err)
	}
}

func TestQuestionEngineFailureHasCause(t *testing.T) {
	saved := engineClient
	t.Cleanup(func() { engineClient = saved })
	socket := filepath.Join(t.TempDir(), "engine.sock")
	engineClient = func() *engineclient.Client {
		return engineclient.New(socket, engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	err := questionCall(t.Context(), "AnswerQuestion", []any{"room", "q1", "", "done"}, nil)
	if err == nil || !strings.Contains(err.Error(), "question change failed") || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("engine failure = %v", err)
	}
}
