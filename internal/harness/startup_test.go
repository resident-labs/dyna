package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestCodexStartupLimit(t *testing.T) {
	for _, mode := range []string{"thread-only", "turn-completes", "turn-canceled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			openFIFO := func(name string) *os.File {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(path, os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { f.Close() })
				return f
			}
			control := openFIFO("control")
			events := openFIFO("events")
			observed := make(chan struct{})
			go func() {
				scanner := bufio.NewScanner(events)
				for scanner.Scan() {
					var event workeroutput.Event
					if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Kind == "status" &&
						((mode == "thread-only" && event.Name == "thread") || event.Name == "turn") {
						// Normalization follows state observation, so the startup
						// limit cannot race ahead of the record under test.
						close(observed)
						return
					}
				}
			}()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fired := make(chan time.Time)
			done := make(chan attempt, 1)
			p := profile.Profile{Harness: profile.HarnessCustom, Env: map[string]string{
				"STARTUP_MODE": mode, "STARTUP_CONTROL": control.Name(),
			}}
			spec := commandSpec{argv: []string{"/bin/sh", "-c", `
printf '%s\n' '{"type":"thread.started","thread_id":"thread-startup"}'
if [ "$STARTUP_MODE" != thread-only ]; then
  printf '%s\n' '{"type":"turn.started"}'
fi
read -r command < "$STARTUP_CONTROL"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"done"}}'
`}}
			go func() {
				done <- runOnce(ctx, p, dir, spec, newSessionTracker(nil), nil, nil, false, "", events.Name(),
					&workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL}, nil, nil, &runState{},
					ExecutionOptions{SkipComputer: true, startupFired: fired})
			}()
			select {
			case <-observed:
			case got := <-done:
				t.Fatalf("worker ended before startup observation: %+v", got)
			case <-time.After(30 * time.Second):
				t.Fatal("worker did not emit its startup records")
			}
			select {
			case fired <- time.Now():
			case got := <-done:
				t.Fatalf("worker ended before the injected startup limit: %+v", got)
			case <-time.After(30 * time.Second):
				t.Fatal("worker did not receive the injected startup limit")
			}
			if mode == "turn-completes" {
				if _, err := control.WriteString("finish\n"); err != nil {
					t.Fatal(err)
				}
			} else if mode == "turn-canceled" {
				cancel()
			}
			select {
			case got := <-done:
				if got.startupStalled != (mode == "thread-only") {
					t.Fatalf("startupStalled = %v, mode = %s: %v", got.startupStalled, mode, got.runErr)
				}
				if mode == "turn-completes" && (got.runErr != nil || !strings.Contains(got.stdout, `"text":"done"`)) {
					t.Fatalf("accepted turn did not complete: %+v", got)
				}
				if mode == "turn-canceled" && (!got.contextDone || got.runErr == nil) {
					t.Fatalf("accepted turn did not obey cancellation: %+v", got)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("worker did not stop")
			}
		})
	}
}

func TestCodexTurnStartPreservesNoProgressLimit(t *testing.T) {
	state := &runState{}
	turn := workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL}, `{"type":"turn.started"}`)
	state.observeRecord(turn)
	if state.hasRealProgress() || state.hasActivity() {
		t.Fatal("turn start counted as output")
	}
	step := workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectOpenCodeJSON},
		`{"type":"step_start","part":{"type":"step-start"}}`)
	state.observeRecord(step)
	state.observeRecord(turn)
	state.observeRecord(step)
	if !state.stepsStalled(2) || state.hasRealProgress() {
		t.Fatal("turn start changed the empty-step limit")
	}
}
