package call

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestCallCLI(t *testing.T) {
	storetest.Isolate(t)
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "calls"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvRoom, rec.Name)
	svc := notify.Default()
	snd := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "RequestCall", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, room, dir, reason string) (callrequests.Request, error) {
			return svc.RequestCall(snd, room, reason)
		}},
		{Name: "WithdrawCall", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, room, dir, id string) error { return svc.WithdrawCall(snd, room, id) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)

	run := func(args ...string) (string, error) {
		c := NewCommand()
		var b bytes.Buffer
		c.SetOut(&b)
		c.SetErr(&b)
		c.SetArgs(args)
		err := c.Execute()
		return b.String(), err
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("missing reason: %v", err)
	}
	if _, err := run("--reason", "   "); err == nil {
		t.Fatal("accepted empty reason")
	}
	out, err := run("--reason", "Owner must sign in")
	if err != nil || out != "call c1 ringing\n" {
		t.Fatal(out, err)
	}
	out, err = run("list", "--json")
	var requests []callrequests.Request
	if err != nil {
		t.Fatal(out, err)
	}
	if err := json.Unmarshal([]byte(out), &requests); err != nil || len(requests) != 1 || requests[0].Reason != "Owner must sign in" {
		t.Fatalf("list: %s, %v", out, err)
	}
	if _, err := run("withdraw", "c1"); err != nil {
		t.Fatal(err)
	}
	req, err := notify.Default().CallRequestStore().Get(rec.ID, "c1")
	if err != nil || req.State != "withdrawn" {
		t.Fatalf("withdraw: %+v, %v", req, err)
	}
	out, err = run("--reason", "Owner action", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	if err := json.Unmarshal([]byte(out), &req); err != nil || req.ID != "c2" {
		t.Fatalf("request JSON: %s, %v", out, err)
	}
}
