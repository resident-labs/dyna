package engine

import (
	"net"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestRunAgentCallsAdmitOnlyTheRunsCoordinator(t *testing.T) {
	calls, err := websvc.Bind(RunAgentBindings(func() *subagent.Manager { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	caller := websvc.Caller{Kind: websvc.Worker, Run: "wf_other"}
	enginetest.ServeCallers(t, t.TempDir(), func(net.Conn) (websvc.Caller, error) { return caller, nil }, calls)
	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	for name, c := range map[string]websvc.Caller{
		"another run":     {Kind: websvc.Worker, Run: "wf_other"},
		"an agent":        {Kind: websvc.Worker, Run: "wf_1", Agent: 2},
		"a session":       {Kind: websvc.Session, Session: "ses_20260101-000000-aaaaaaaa"},
		"the owner shell": {Kind: websvc.Owner},
	} {
		caller = c
		for method, args := range map[string][]any{
			"StartRunAgent":  {callspec.RunAgentStart{Run: "wf_1", Agent: 1}},
			"WaitRunAgent":   {callspec.RunAgentRef{Run: "wf_1", Agent: 1}},
			"CloseRunAgents": {"wf_1"},
		} {
			err := client.Call(t.Context(), method, args, nil)
			if err == nil || !strings.Contains(err.Error(), "verified coordinator") {
				t.Errorf("%s by %s = %v; want a coordinator refusal", method, name, err)
			}
		}
	}
}
