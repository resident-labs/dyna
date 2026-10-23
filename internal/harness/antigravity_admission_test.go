package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/acp"
	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
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
	p, mark := antigravityACPTestProfile(t, dir)
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

		waitActive, cancelActive := context.WithCancel(ctx)
		concurrent, blocked := harnessruntime.NewAccounts().TurnLeaseManaged(cancelAtAccountWait{waitActive, cancelActive}, p.Harness)
		cancelActive()
		if concurrent != nil {
			concurrent.Close()
			t.Fatal("active turn lost exclusive protection")
		}
		if !errors.Is(blocked, context.Canceled) {
			t.Fatalf("active admission: %v", blocked)
		}
		s.settled = true
		s.close()
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}

		// An unrelated session can acquire the account while this child is idle.
		wait, cancel := context.WithCancel(ctx)
		other, err := harnessruntime.NewAccounts().TurnLeaseManaged(cancelAtAccountWait{wait, cancel}, p.Harness)
		cancel()
		if err != nil {
			t.Fatalf("idle child blocks another session: %v", err)
		}
		if err := other.Close(); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "boot ") != 1 {
		t.Fatalf("same session restarted its retained child: %s", body)
	}
}
