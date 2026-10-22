package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

type admissionCall struct {
	ID    string
	Input callspec.AccountAdmission
}

func admissionOutbox(t *testing.T, prefix string, handle func(http.ResponseWriter, *http.Request, admissionCall)) *engineclient.Outbox {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/AdmitNativeAttempt" {
			_, _ = w.Write([]byte("null"))
			return
		}
		var args []callspec.AccountAdmission
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 {
			t.Errorf("admission arguments: %v, %v", args, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		handle(w, r, admissionCall{r.Header.Get(callspec.RequestHeader), args[0]})
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	out := engineclient.NewOutbox(engineclient.New(listener.Addr().String(), engineclient.RestartWait(0), engineclient.Notices(nil)), prefix)
	out.Logf = t.Logf
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out.Close(ctx)
	})
	return out
}

// admissionProfile returns a Codex profile whose fake app-server logs each
// spawn and request. The log file exists only after a process starts.
func admissionProfile(t *testing.T) (profile.Profile, string) {
	t.Helper()
	p, log := installFakeCodex(t, "normal")
	home := t.TempDir()
	t.Setenv("DYNA_DATA_DIR", home)
	t.Setenv("XDG_DATA_HOME", home)
	return p, log
}

// codexTurnStarts counts turn/start requests in the fake log. Each admitted
// attempt starts one turn, so the count matches the admitted attempts.
func codexTurnStarts(t *testing.T, log string) int {
	t.Helper()
	return strings.Count(readTestFile(t, log), `"method":"turn/start"`)
}

func runAdmission(ctx context.Context, p profile.Profile, cwd string, out *engineclient.Outbox) (Result, error) {
	return runJournaled(ctx, p, "task", cwd, true, 0, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{
		Execution: callspec.ExecutionRef{Session: "session", Turn: 7}, Outbox: out,
	})
}

func assertNoAdmissionProcess(t *testing.T, log string) {
	t.Helper()
	if raw, err := os.ReadFile(log); !os.IsNotExist(err) {
		t.Fatalf("native process started: %q, %v", raw, err)
	}
}

func TestNativeAdmissionRejects(t *testing.T) {
	for _, kind := range []string{"old engine", "refused", "null", "empty object", "empty", "wrong parent", "wrong ID", "incomplete", "bad type"} {
		t.Run(kind, func(t *testing.T) {
			p, log := admissionProfile(t)
			out := admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
				assertNoAdmissionProcess(t, log)
				ref := call.Input.ExecutionRef
				ref.AdmissionID = call.ID
				switch kind {
				case "null":
					_, _ = w.Write([]byte(`null`))
					return
				case "empty object":
					_, _ = w.Write([]byte(`{}`))
					return
				case "old engine":
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "no method"})
					return
				case "refused":
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "native admission refused"})
					return
				case "empty":
					ref.AdmissionID = ""
				case "wrong parent":
					ref.Turn++
				case "wrong ID":
					ref.AdmissionID = "server-supplied/999"
				case "incomplete":
					ref.Session = ""
				case "bad type":
					_, _ = w.Write([]byte(`{"turn":"bad"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(ref)
			})
			result, err := runAdmission(t.Context(), p, t.TempDir(), out)
			if err == nil {
				t.Fatalf("started without valid admission: %+v", result)
			}
			assertNoAdmissionProcess(t, log)
			if result.Execution != (callspec.ExecutionRef{}) {
				t.Fatalf("invalid reference escaped: %+v", result.Execution)
			}
			if kind == "null" || kind == "empty object" || kind == "empty" || kind == "incomplete" || kind == "bad type" || kind == "wrong parent" || kind == "wrong ID" {
				var uncertain *engineclient.UnacknowledgedError
				if !errors.As(err, &uncertain) || uncertain.RequestID != "session/7/123/456/1" || !strings.Contains(err.Error(), uncertain.RequestID) {
					t.Fatalf("lost uncertain request: %v", err)
				}
			}
		})
	}
}

func TestNativeAdmissionRecoveryIDs(t *testing.T) {
	p, log := admissionProfile(t)
	// The first turn fails with a transport error, so the attempt is retried.
	p.Env[envFakeCodexFailTurns] = "1"
	var mu sync.Mutex
	var calls []admissionCall
	out := admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
		mu.Lock()
		calls = append(calls, call)
		count := len(calls)
		mu.Unlock()
		if count == 1 {
			assertNoAdmissionProcess(t, log)
		} else if got := codexTurnStarts(t, log); got != count-1 {
			t.Errorf("turns before admission %d: %d", count, got)
		}
		ref := call.Input.ExecutionRef
		ref.AdmissionID = call.ID
		_ = json.NewEncoder(w).Encode(ref)
	})
	out.Send("Before")
	cwd := t.TempDir()
	result, err := runAdmission(t.Context(), p, cwd, out)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0].ID != "session/7/123/456/2" || calls[1].ID != "session/7/123/456/3" {
		t.Fatalf("ordinary request IDs: %+v", calls)
	}
	for _, call := range calls {
		if call.Input.Profile != p.Name || call.Input.Harness != p.Harness || call.Input.WorkDir != cwd || call.Input.ExecutionRef != (callspec.ExecutionRef{Session: "session", Turn: 7}) {
			t.Fatalf("prepared admission: %+v", call)
		}
	}
	want := calls[1].Input.ExecutionRef
	want.AdmissionID = calls[1].ID
	if result.Execution != want || !result.Nudged || codexTurnStarts(t, log) != 2 {
		t.Fatalf("native attempts: %+v", result)
	}
}

func TestNativeAdmissionLostAck(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact_%t", compact), func(t *testing.T) {
			p, log := admissionProfile(t)
			steering := SteeringOptions{}
			session := SessionOptions{}
			if compact {
				log, steering = installOpenCodeCompactCLI(t)
				p.Harness = profile.HarnessOpenCode
				p.Env["DYNA_FAKE_LOG"] = log
				p.Compact = &profile.Compact{Tokens: 120}
				session.ResumeID = "ses_fixture"
			}
			invoke := func(ctx context.Context, out *engineclient.Outbox) (Result, error) {
				return runJournaled(ctx, p, "task", t.TempDir(), true, 0, JournalOptions{}, steering, session, ExecutionOptions{
					Execution: callspec.ExecutionRef{Session: "session", Turn: 7}, Outbox: out,
				})
			}
			accounts := &harnessruntime.Accounts{Root: filepath.Join(t.TempDir(), "accounts")}
			prepare := PrepareRuntime
			PrepareRuntime = func(ctx context.Context, p profile.Profile, previous *harnessruntime.Pin) (profile.Profile, harnessruntime.Pin, func(), error) {
				selected, pin, release, err := prepare(ctx, p, previous)
				if err != nil {
					return selected, pin, release, err
				}
				lease, err := accounts.TurnLeaseManaged(ctx, p.Harness)
				if err != nil {
					release()
					return selected, pin, nil, err
				}
				pin.AccountID = "prepared-account"
				return selected, pin, func() { _ = lease.Close(); release() }, nil
			}
			t.Cleanup(func() { PrepareRuntime = prepare })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			facts := map[string]callspec.AccountAdmission{}
			out := admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
				mu.Lock()
				facts[call.ID] = call.Input
				mu.Unlock()
				cancel() // The fact committed, but its acknowledgement never arrives.
				<-r.Context().Done()
			})
			var spooled []engineclient.Queued
			out.Spool = func(queued []engineclient.Queued) error { spooled = append(spooled, queued...); return nil }
			_, err := invoke(ctx, out)
			var uncertain *engineclient.UnacknowledgedError
			if !errors.As(err, &uncertain) || uncertain.RequestID != "session/7/123/456/1" || !strings.Contains(err.Error(), uncertain.RequestID) {
				t.Fatalf("lost acknowledgement: %v", err)
			}
			assertNoAdmissionProcess(t, log)
			if _, running := accounts.SwitchStatus(p.Harness); running != 0 {
				t.Fatalf("preparation lease leaked: %d", running)
			}
			out.Close(ctx)
			if len(spooled) != 1 || spooled[0].ID != uncertain.RequestID {
				t.Fatalf("lost original outbox entry: %+v", spooled)
			}
			next := admissionOutbox(t, "session/7/124/789", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
				if call.ID == uncertain.RequestID || call.Input.AccountID != "prepared-account" {
					t.Errorf("next invocation reused uncertain request or lost its prepared account: %+v", call)
				}
				if _, running := accounts.SwitchStatus(p.Harness); running != 1 {
					t.Errorf("next invocation lacks preparation lease: %d", running)
				}
				ref := call.Input.ExecutionRef
				ref.AdmissionID = call.ID
				_ = json.NewEncoder(w).Encode(ref)
			})
			result, err := invoke(t.Context(), next)
			if err != nil || !strings.HasPrefix(result.Output, "done") {
				t.Fatalf("next invocation: %+v, %v", result, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(facts) != 1 || facts[uncertain.RequestID].AccountID != "prepared-account" || facts[uncertain.RequestID].ExecutionRef != (callspec.ExecutionRef{Session: "session", Turn: 7}) {
				t.Fatalf("uncertain fact changed: %+v", facts)
			}
			if _, running := accounts.SwitchStatus(p.Harness); running != 0 {
				t.Fatalf("next preparation lease leaked: %d", running)
			}
		})
	}
}

func TestNativeAdmissionNoFailover(t *testing.T) {
	p, log := admissionProfile(t)
	oldFailover := FailoverAccount
	FailoverAccount = func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		t.Error("admission refusal entered account failover")
		return nil, nil
	}
	t.Cleanup(func() { FailoverAccount = oldFailover })
	out := admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "account quota or usage limit forbids admission"})
	})
	result, err := runAdmission(t.Context(), p, t.TempDir(), out)
	if err == nil || result.Nudged || result.AccountSwitch != nil || strings.Contains(err.Error(), "session needs another model") {
		t.Fatalf("admission entered native failure handling: %+v, %v", result, err)
	}
	var remote *engineclient.RemoteError
	if !errors.As(err, &remote) || remote.Status != http.StatusForbidden {
		t.Fatalf("lost refusal cause: %v", err)
	}
	assertNoAdmissionProcess(t, log)
}

func TestNativeAdmissionNeedsParent(t *testing.T) {
	for _, kind := range []string{"missing outbox", "missing parent", "already admitted", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			p, log := admissionProfile(t)
			out := admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
				t.Error("invalid execution reached the engine")
				http.Error(w, "unexpected", http.StatusForbidden)
			})
			exec := ExecutionOptions{Execution: callspec.ExecutionRef{Session: "session", Turn: 7}, Outbox: out}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch kind {
			case "missing outbox":
				exec.Outbox = nil
			case "missing parent":
				exec.Execution = callspec.ExecutionRef{}
			case "already admitted":
				exec.Execution.AdmissionID = "old/1"
			case "canceled":
				cancel()
			}
			_, err := runJournaled(ctx, p, "task", t.TempDir(), true, 0, JournalOptions{}, SteeringOptions{}, SessionOptions{}, exec)
			if err == nil {
				t.Fatal("invalid execution launched")
			}
			assertNoAdmissionProcess(t, log)
		})
	}
}

func TestNativeAdmissionCompactionRefuses(t *testing.T) {
	for _, denyAt := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("deny_%d", denyAt), func(t *testing.T) {
			testNativeAdmissionCompaction(t, denyAt)
		})
	}
}

func TestNativeAdmissionCompactionAdmits(t *testing.T) {
	testNativeAdmissionCompaction(t, 0)
}

func testNativeAdmissionCompaction(t *testing.T, denyAt int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("DYNA_DATA_DIR", home)
	log, steering := installOpenCodeCompactCLI(t)
	p := profile.Profile{Name: "oc-fake", Harness: profile.HarnessOpenCode, Compact: &profile.Compact{Tokens: 120}, Env: map[string]string{"DYNA_FAKE_LOG": log}}
	calls := make(chan admissionCall, 8)
	out := admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
		calls <- call
		n := len(calls)
		raw, err := os.ReadFile(log)
		if n == 1 {
			if !os.IsNotExist(err) {
				t.Errorf("process before first admission: %q, %v", raw, err)
			}
		} else if err != nil || strings.Count(string(raw), "\n") != n-1 {
			t.Errorf("processes before admission %d: %q, %v", n, raw, err)
		}
		if n == denyAt {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "compaction admission refused"})
			return
		}
		ref := call.Input.ExecutionRef
		ref.AdmissionID = call.ID
		_ = json.NewEncoder(w).Encode(ref)
	})
	result, err := runJournaled(t.Context(), p, "task", t.TempDir(), true, 0, JournalOptions{}, steering, SessionOptions{ResumeID: "ses_fixture"}, ExecutionOptions{
		Execution: callspec.ExecutionRef{Session: "session", Turn: 7}, Outbox: out,
	})
	want := denyAt
	if denyAt == 0 {
		want = 5
		if err != nil || result.Output != "done" {
			t.Fatalf("successful compact turn: %+v, %v", result, err)
		}
	} else if err == nil || !isNativeAdmissionError(err) || result.CompactError != "" {
		t.Fatalf("compaction swallowed admission failure: %+v, %v", result, err)
	}
	if len(calls) != want {
		t.Fatalf("admission count = %d, want %d", len(calls), want)
	}
	lastAdmitted := want
	if denyAt != 0 {
		lastAdmitted--
	}
	var expected callspec.ExecutionRef
	if lastAdmitted > 0 {
		expected = callspec.ExecutionRef{Session: "session", Turn: 7, AdmissionID: fmt.Sprintf("session/7/123/456/%d", lastAdmitted)}
	}
	if result.Execution != expected {
		t.Fatalf("last admitted reference = %+v, want %+v", result.Execution, expected)
	}
	for i := 1; i <= want; i++ {
		call := <-calls
		if call.ID != fmt.Sprintf("session/7/123/456/%d", i) {
			t.Fatalf("nonordinary ID: %+v", call)
		}
	}
	if denyAt == 1 {
		assertNoAdmissionProcess(t, log)
	}
}

func TestNativeAdmissionSkipsOtherExecution(t *testing.T) {
	for _, injected := range []bool{false, true} {
		t.Run(fmt.Sprintf("injected_%t", injected), func(t *testing.T) {
			p, _ := admissionProfile(t)
			exec := ExecutionOptions{Execution: callspec.ExecutionRef{Session: "session", Turn: 7}}
			calls := make(chan admissionCall, 1)
			exec.Outbox = admissionOutbox(t, "session/7/123/456", func(w http.ResponseWriter, r *http.Request, call admissionCall) {
				calls <- call
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "non-native execution must not request account admission"})
			})
			ran := false
			if injected {
				exec.ProcessRunner = func(_ context.Context, _ []string, stdout, _ io.Writer) error {
					ran = true
					if _, err := fmt.Fprintln(stdout, `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`); err != nil {
						return err
					}
					_, err := fmt.Fprintln(stdout, `{"type":"turn.completed"}`)
					return err
				}
			} else {
				p.Harness = profile.HarnessCustom
				p.Command = []string{"/bin/sh", "-c", "printf done"}
			}
			result, err := runJournaled(t.Context(), p, "task", t.TempDir(), true, 0, JournalOptions{}, SteeringOptions{}, SessionOptions{}, exec)
			if err != nil || result.Output != "done" || (injected && !ran) {
				t.Fatalf("non-native execution: %+v, %v; injected runner called: %t", result, err, ran)
			}
			if len(calls) != 0 || result.Execution != (callspec.ExecutionRef{}) {
				t.Fatalf("non-native execution used admission: %+v", result)
			}
		})
	}
}
