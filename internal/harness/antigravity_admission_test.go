package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/acp"
	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/store/storetest"
)

// cancelAtAccountWait cancels only when account admission needs to wait.
type cancelAtAccountWait struct {
	context.Context
	cancel context.CancelFunc
}

func (c cancelAtAccountWait) Done() <-chan struct{} {
	c.cancel()
	return c.Context.Done()
}

func TestAntigravityRetainedAdmission(t *testing.T) {
	storetest.DataDir(t)
	dir := acpScript(t, map[string]string{"task": `{"stopReason":"end_turn"}`})
	p, _ := antigravityACPTestProfile(t, dir)
	scope, err := harnessruntime.NewAccounts().HarnessHome(p.Harness)
	if err != nil {
		t.Fatal(err)
	}
	p.Env["HOME"] = harnesshome.Home(scope)
	token := filepath.Join(p.Env["HOME"], "antigravity-acp", "acp_token.json")
	if err := os.MkdirAll(filepath.Dir(token), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	nativeID := ""
	for range 2 {
		lease, err := prepareAccountTurnLease(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		p.RuntimeBindProcess = lease.Bind
		s, err := acpDial(ctx, p, dir, nativeID, ExecutionOptions{HealthProbe: true}, nil, nil)
		if err != nil {
			lease.Close()
			t.Fatal(err)
		}
		nativeID = s.session
		_, err = s.agent.Prompt(ctx, s.session, []acp.PromptBlock{acp.TextBlock("task")})
		if err != nil {
			s.close()
			lease.Close()
			t.Fatal(err)
		}
		s.settled = true
		s.close()
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	key, err := sessionKey(p, ExecutionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	home := p.Env["HOME"]
	_, found, err := sessionhost.RetainedReference(ctx, nativeSessionHost, sessionhost.Launch{
		Harness: p.Harness, Key: key + "-other" + "\x00" + home + "\x00" + home,
	})
	if err != nil || found {
		t.Fatalf("another session borrowed the retained child: %v, %v", found, err)
	}
	wait, cancel := context.WithCancel(ctx)
	defer cancel()
	lease, err := harnessruntime.NewAccounts().TurnLeaseManaged(cancelAtAccountWait{wait, cancel}, p.Harness)
	if lease != nil {
		lease.Close()
		t.Fatal("another session bypassed the exclusive account lease")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("account admission did not block: %v", err)
	}
}
