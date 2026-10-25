package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/state"
)

// The engine posts the provider notice after committing the turn. A resend
// after a crash must not restore a notice the owner has already dismissed.
func TestTurnEndPostsCooldownNoticeOnce(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if err := state.MigrateRequests(filepath.Dir(e.m.store().root())); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	end := TurnEnd{Turn: 1, Status: TurnError, Error: "429 usage limit reached", FailureReason: providerfailure.Quota, Ended: time.Now()}
	request := func() context.Context { return state.ForRequest(actor, "runner/cooldown") }
	if err := e.m.RunnerTurnEnd(request(), end); err != nil {
		t.Fatal(err)
	}
	notices, err := e.m.notices().List()
	if err != nil || len(notices) != 1 {
		t.Fatalf("notices = %+v, %v; want one provider notice", notices, err)
	}
	if notices[0].Key != "provider:mock" || !notices[0].At.Equal(end.Ended) {
		t.Fatalf("notice = %+v; want the stored turn's profile and end time", notices[0])
	}
	if err := e.m.notices().Dismiss(notices[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnEnd(request(), end); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("resend = %v; want ErrRepeated", err)
	}
	if notices, err := e.m.notices().List(); err != nil || len(notices) != 0 {
		t.Fatalf("notices after resend = %+v, %v; want the notice to stay dismissed", notices, err)
	}
}

func TestTurnEndCooldownUsesTheStoredOutcome(t *testing.T) {
	for _, which := range []string{"ordinary-error", "stopped", "no-profile", "startup-timeout"} {
		t.Run(which, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			end := TurnEnd{Turn: 1, Status: TurnError, Error: "429 usage limit reached", FailureReason: providerfailure.Quota, Ended: time.Now()}
			switch which {
			case "ordinary-error":
				end.Error, end.FailureReason = "invalid input", ""
			case "stopped":
				end.Status = TurnStopped
			case "no-profile":
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Profile = "" }); err != nil {
					t.Fatal(err)
				}
			case "startup-timeout":
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.StopReason = transferStartupTimeout }); err != nil {
					t.Fatal(err)
				}
			}
			actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
			if err := e.m.RunnerTurnEnd(actor, end); err != nil {
				t.Fatal(err)
			}
			if notices, err := e.m.notices().List(); err != nil || len(notices) != 0 {
				t.Fatalf("notices = %+v, %v; want none", notices, err)
			}
		})
	}
}

// A runner may make only its own session dormant, for the turn still stored on
// it. Zero must never become a turn.
func TestRunnerReturnDormantChecksTheStoredTurn(t *testing.T) {
	for _, which := range []string{"finished", "stale-turn", "zero-turn", "running", "goal", "no-caller"} {
		t.Run(which, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			if which == "running" {
				lock, err := e.m.Store.tryTurnLock(info.ID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { lock.Close() })
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Status = TurnRunning }); err != nil {
					t.Fatal(err)
				}
			}
			if which == "goal" {
				if _, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "New work", Holders: []string{info.ID}}); err != nil {
					t.Fatal(err)
				}
			}
			ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
			turn := 1
			switch which {
			case "stale-turn":
				turn = 2
			case "zero-turn":
				turn = 0
			case "no-caller":
				ctx = t.Context()
			}
			err := e.m.RunnerReturnDormant(ctx, turn)
			refused := which == "zero-turn" || which == "no-caller"
			if (err != nil) != refused {
				t.Fatalf("return dormant = %v; want refusal %v", err, refused)
			}
			meta, err := e.m.Store.ReadMeta(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := meta.Status == StatusDormant; got != (which == "finished") {
				t.Fatalf("status = %s; want dormant only after a turn that left no work", meta.Status)
			}
		})
	}
}

// A detached runner sends the return through its outbox after final usage.
// A successful engine reply must not cause a second, local finish.
func TestRunnerReturnsDormantThroughTheOutbox(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	methods := make(chan string, 2)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		methods <- method
		if id := r.Header.Get(callspec.RequestHeader); !strings.HasPrefix(id, info.ID+"/") {
			t.Errorf("request ID = %q; want the caller's session prefix", id)
		}
		if method == "RunnerReturnDormant" {
			var args []int
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 || args[0] != 1 {
				t.Errorf("return dormant args = %v, %v; want turn 1", args, err)
			}
		}
		_, _ = w.Write([]byte("null"))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	e.m.Engine = engineclient.New(path)
	writes := e.m.runnerWrites(info.ID, 1)
	t.Cleanup(func() { writes.close(t.Context()) })
	writes.finish(RunnerFinish{Turn: 1})
	if err := writes.returnDormant(t.Context()); err != nil {
		t.Fatal(err)
	}
	if first, second := <-methods, <-methods; first != "RunnerFinish" || second != "RunnerReturnDormant" {
		t.Fatalf("calls = %s, %s; want usage before return dormant", first, second)
	}
	if meta, err := e.m.Store.ReadMeta(info.ID); err != nil || meta.Status == StatusDormant {
		t.Fatalf("runner wrote a local finish: status %s, %v", meta.Status, err)
	}
}
