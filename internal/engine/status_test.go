package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestStatusPrintsHealth(t *testing.T) {
	started := time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)
	want := Health{Started: started, Degraded: true, SQLiteBusyRetries: 3, SweepFindings: 2,
		Writes: state.Health{Writes: 12, P50: 3 * time.Millisecond, P99: 40 * time.Millisecond, Slowest: 120 * time.Millisecond, SlowestKind: "posts", InFlight: 2, Oldest: 6 * time.Second, Degraded: true}, WritersWaiting: 1,
		Events: hub.Stats{Seq: 1234, Streams: 3, MaxLag: 2, Dropped: 1}}
	call := func(_ context.Context, method string, _ []any, result any) error {
		if method != "Health" {
			return fmt.Errorf("called %s", method)
		}
		// Through JSON, as over the engine socket.
		b, err := json.Marshal(want)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, result)
	}
	var out bytes.Buffer
	if err := Status(context.Background(), call, &out, started.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantOut := `engine: degraded
started: 2026-10-20 13:00:00 (1h0m0s ago)
writes, last 5 min: 12, p50 3ms, p99 40ms, slowest 120ms (posts)
writes in flight: 2, oldest 6s; waiting for the writer lock: 1
events: revision 1234, 3 streams, largest lag 2, 1 dropped
sqlite busy retries: 3
database duration counts since start (<=1ms, <=10ms, <=100ms, <=1s, >1s): hold [0 0 0 0 0], commit [0 0 0 0 0], checkpoint [0 0 0 0 0]
scheduled checkpoints since start: total frames observed 0, copied 0, incomplete 0, errors 0
callers refused: 0
safety sweep findings: 2
`
	if out.String() != wantOut {
		t.Fatalf("status printed:\n%s\nwant:\n%s", out.String(), wantOut)
	}
}

func TestStatusReportsAnEngineThatDoesNotAnswer(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	hang := func(ctx context.Context, _ string, _ []any, _ any) error { <-ctx.Done(); return ctx.Err() }
	err := Status(ctx, hang, &bytes.Buffer{}, time.Now())
	if err == nil || !strings.HasPrefix(err.Error(), "engine not answering") {
		t.Fatalf("hung engine reported as %v", err)
	}
	down := func(context.Context, string, []any, any) error { return engineclient.ErrNotRunning }
	if err := Status(context.Background(), down, &bytes.Buffer{}, time.Now()); err == nil || err.Error() != "engine not running" {
		t.Fatalf("stopped engine reported as %v", err)
	}
}

// A writable engine that refuses Connect still takes posts: the refusal is
// its own field and line, and Status (shown as "Posting is off") stays empty.
func TestConnectRefusalIsNotTheReadOnlyStatus(t *testing.T) {
	note := "not joining Resident Labs Connect: the engine does not run in dyna-engine.service"
	h := engineHealth("", note, time.Now(), nil, hub.New(1), nil, nil)()
	if h.Status != "" || h.Connect != note {
		t.Fatalf("Health = status %q, connect %q", h.Status, h.Connect)
	}
	call := func(_ context.Context, _ string, _ []any, result any) error {
		b, err := json.Marshal(h)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, result)
	}
	var out bytes.Buffer
	if err := Status(context.Background(), call, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "engine: ok\nconnect: "+note+"\n") {
		t.Fatalf("status printed:\n%s", out.String())
	}
}

func TestWaitHealthyAfterARestart(t *testing.T) {
	answers := func(replies ...any) func(context.Context, string, []any, any) error {
		return func(_ context.Context, _ string, _ []any, result any) error {
			reply := replies[0]
			if len(replies) > 1 {
				replies = replies[1:]
			}
			if err, ok := reply.(error); ok {
				return err
			}
			b, err := json.Marshal(reply)
			if err != nil {
				return err
			}
			return json.Unmarshal(b, result)
		}
	}
	pauses := 0
	pause := func(context.Context, time.Duration) error {
		if pauses++; pauses > 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	ctx := context.Background()
	if err := WaitHealthy(ctx, answers(Health{Status: healthStarting}, Health{Ready: true}), pause); err != nil || pauses != 1 {
		t.Fatalf("a migrating engine that becomes ready gave %v after %d pauses", err, pauses)
	}
	pauses = 0
	if err := WaitHealthy(ctx, answers(engineclient.ErrNotRunning, engineclient.ErrNotRunning, Health{}), pause); err != nil || pauses != 2 {
		t.Fatalf("an engine that comes up gave %v after %d pauses", err, pauses)
	}
	pauses = 0
	err := WaitHealthy(ctx, answers(Health{Status: "read-only: migrate dyna.db: boom"}), pause)
	if err == nil || err.Error() != "the engine is up, but read-only: migrate dyna.db: boom" {
		t.Fatalf("a failed migration gave %v", err)
	}
	err = WaitHealthy(ctx, answers(Health{Status: healthStarting}), func(context.Context, time.Duration) error { return context.DeadlineExceeded })
	if err == nil || err.Error() != "the engine did not become ready: context deadline exceeded" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a starting engine that timed out gave %v", err)
	}
	pauses = 0
	if err := WaitHealthy(ctx, answers(engineclient.ErrNotRunning), pause); err == nil || !strings.HasPrefix(err.Error(), "the engine did not answer Health") {
		t.Fatalf("an engine that never answers gave %v", err)
	}
}

// 'dyna engine status' is the first command people try from a refused
// shell, so it prints the refusal's cause, not a generic failure.
func TestStatusShowsARefusal(t *testing.T) {
	const line = "the engine refused this call: rule (c): the caller runs in the engine's cgroup, but Dyna did not start it for a session"
	call := func(context.Context, string, []any, any) error { return errors.New(line) }
	err := Status(context.Background(), call, io.Discard, time.Now())
	if err == nil || !strings.Contains(err.Error(), line) {
		t.Fatalf("Status = %v, want the refusal line", err)
	}
}
