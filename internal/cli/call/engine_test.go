package call

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/websvc"
)

func TestCallWritesGoThroughEngine(t *testing.T) {
	var requests []string
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "RequestCall", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, room, dir, reason string) (callrequests.Request, error) {
			if dir == "" {
				t.Error("caller working directory is missing")
			}
			requests = append(requests, "request:"+room+":"+reason)
			return callrequests.Request{ID: "c9", Room: room, Reason: reason}, nil
		}},
		{Name: "WithdrawCall", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, room, dir, id string) error {
			requests = append(requests, "withdraw:"+room+":"+id)
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, t.TempDir(), calls)
	run := func(args ...string) (string, error) {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("--room", "calls", "--reason", "Owner action")
	if err != nil || out != "call c9 ringing\n" {
		t.Fatalf("request: %q, %v", out, err)
	}
	if _, err := run("withdraw", "c9", "--room", "calls"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(requests, "|") != "request:calls:Owner action|withdraw:calls:c9" {
		t.Fatalf("engine calls: %v", requests)
	}
}

func TestCallWithoutEngineFailsWithCause(t *testing.T) {
	saved := engineClient
	t.Cleanup(func() { engineClient = saved })
	engineClient = func() *engineclient.Client {
		return engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	for _, args := range [][]string{{"--room", "calls", "--reason", "Owner action"}, {"withdraw", "c1", "--room", "calls"}} {
		cmd := NewCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		if !errors.Is(err, engineclient.ErrNotRunning) || !strings.Contains(err.Error(), "call request was not") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
