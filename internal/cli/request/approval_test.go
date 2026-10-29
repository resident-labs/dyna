package request

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestApprovalDecisionCLIUsesEngine(t *testing.T) {
	storetest.DataDir(t)
	storetest.ClearStamp(t)
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "approval-cli"})
	if err != nil {
		t.Fatal(err)
	}
	var decisions []approvals.DecisionInput
	cancelled := ""
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "ApprovalDecide", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, in approvals.DecisionInput) (approvals.Request, error) {
			decisions = append(decisions, in)
			return approvals.Request{}, nil
		}},
		{Name: "ApprovalCancel", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, ref, id string) (approvals.Request, error) {
			cancelled = ref + " " + id
			return approvals.Request{}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	run := func(args ...string) error {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		return cmd.ExecuteContext(t.Context())
	}
	if err := run("--room", rec.ID, "deny", "A-42", "--reason", "old flag"); err == nil || !strings.Contains(err.Error(), "--why is required") {
		t.Fatalf("deny without --why = %v", err)
	}
	for _, args := range [][]string{
		{"--room", rec.ID, "approve", "A-42"},
		{"--room", rec.ID, "pass", "A-42", "--why", "owner should decide"},
		{"--room", rec.ID, "deny", "A-42", "--why", "not safe"},
		{"--room", rec.ID, "cancel", "A-42"},
	} {
		if err := run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	want := []approvals.DecisionInput{
		{Room: rec.ID, RequestID: "A-42", Action: approvals.ActionApprove},
		{Room: rec.ID, RequestID: "A-42", Action: approvals.ActionPass, Reason: "owner should decide"},
		{Room: rec.ID, RequestID: "A-42", Action: approvals.ActionDeny, Reason: "not safe"},
	}
	if !reflect.DeepEqual(decisions, want) || cancelled != rec.ID+" A-42" {
		t.Fatalf("decisions = %+v, cancel = %q", decisions, cancelled)
	}
}
