package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
)

// hostEngine answers each hostexec.run call with the next step and records the client ID of every call.
func hostEngine(t *testing.T, steps ...http.HandlerFunc) (*Client, func() []string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var mu sync.Mutex
	var ids []string
	go func() {
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req contract.HostexecRunRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			step := steps[min(len(ids), len(steps)-1)]
			ids = append(ids, req.ClientID)
			mu.Unlock()
			step(w, r)
		}))
	}()
	return NewClient(socket), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ids...)
	}
}

func hostAnswer(result *contract.OpResult) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(contract.HostexecRunResponse{Operation: contract.Operation{ID: "op_1", Kind: "hostexec.run", Args: contract.HostexecRunArgs{Room: "r", Host: "h", Argv: []string{"true"}, Timeout: 1000, Grant: "g"}, State: contract.OpDone, Result: result}})
	}
}

func hostDrop(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func hostUnavailable(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"code":"unavailable","message":"shutting down"}`))
}

// The engine goes away during the call (a deploy, or the socket dropping) and answers unavailable while it shuts down.
// The run is the engine's, and it goes on: the job asks again with the same client ID and gets the run's own output and exit status.
func TestHostRunAsksAgainWhenTheEngineGoesAwayAndGetsTheRunsResult(t *testing.T) {
	c, ids := hostEngine(t, hostDrop, hostUnavailable, hostAnswer(&contract.OpResult{ExitCode: 3, Output: "built\n"}))
	var out bytes.Buffer
	code, problem := c.HostRun(context.Background(), shell.Run{Host: "h", Command: "make", ClientID: "run-1"}, &out)
	if problem != nil || code != 3 || out.String() != "built\n" {
		t.Fatalf("code %d, problem %+v, output %q", code, problem, out.String())
	}
	if got := ids(); len(got) != 3 || got[0] != "run-1" || got[1] != "run-1" || got[2] != "run-1" {
		t.Fatalf("client IDs %v", got)
	}
}

// An answer that carries no result is the engine's call ending before the command did, which is not the command's end.
func TestHostRunAsksAgainWhenTheAnswerHasNoResult(t *testing.T) {
	c, ids := hostEngine(t, hostAnswer(nil), hostAnswer(&contract.OpResult{Output: "ok\n"}))
	var out bytes.Buffer
	code, problem := c.HostRun(context.Background(), shell.Run{Host: "h", Command: "true", ClientID: "run-2"}, &out)
	if problem != nil || code != 0 || out.String() != "ok\n" || len(ids()) != 2 {
		t.Fatalf("code %d, problem %+v, output %q, calls %d", code, problem, out.String(), len(ids()))
	}
}

// A refusal is the engine's answer, not a lost call, and ending the context ends the asking.
func TestHostRunDoesNotAskAgainAfterARefusalOrAStop(t *testing.T) {
	refuse := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"no_grant","message":"no matching host-exec grant"}`))
	}
	c, ids := hostEngine(t, refuse)
	_, problem := c.HostRun(context.Background(), shell.Run{Host: "h", Command: "true", ClientID: "run-3"}, &bytes.Buffer{})
	if problem == nil || problem.Code != contract.ErrNoGrant || len(ids()) != 1 {
		t.Fatalf("problem %+v, calls %d", problem, len(ids()))
	}
	c, ids = hostEngine(t, hostUnavailable)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for len(ids()) == 0 {
		}
		cancel()
	}()
	_, problem = c.HostRun(ctx, shell.Run{Host: "h", Command: "true", ClientID: "run-4"}, &bytes.Buffer{})
	if problem == nil || !strings.Contains(problem.Message, "canceled") {
		t.Fatalf("problem %+v", problem)
	}
}
