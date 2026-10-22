package userservices

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func TestEngineRestartWaitsForCallOrDeadline(t *testing.T) {
	for _, end := range []string{"call ends", "deadline", "next install", "worker finished", "reboot"} {
		t.Run(end, func(t *testing.T) {
			storetest.DataDir(t)
			dir := environment(t)
			now := time.Unix(1000, 0)
			started := now
			busy := true
			restarts, workers, health := 0, 0, 0
			changed, shows := false, 0
			locked := func() {
				unlock, owned, err := store.TryLock(engineRestartLock(dir))
				if owned {
					unlock()
				}
				if err != nil || owned {
					t.Fatalf("restart and health check must hold the shared lock: owned=%v err=%v", owned, err)
				}
			}
			var output bytes.Buffer
			o := Options{Binary: "/test/dyna", Dir: dir, Output: &output,
				Now: func() time.Time { return now },
				EngineQuiet: func(context.Context) (string, error) {
					if busy {
						return "a voice call is live", nil
					}
					return "", nil
				},
				Healthy: func(context.Context) error { locked(); health++; return nil },
				Run: func(_ context.Context, args ...string) (string, error) {
					command := strings.Join(args, " ")
					if changed && strings.Contains(command, "show "+Engine) {
						shows++
						if shows == 1 {
							return "InvocationID=old", nil
						}
						restarts++ // The worker completed while the installer took the lock.
						return "InvocationID=new", nil
					}
					if strings.Contains(command, "restart "+Engine) {
						locked()
						restarts++
					}
					if strings.Contains(command, "start "+engineRestartWorker) {
						workers++
					}
					return "", nil
				},
			}
			for range 2 {
				if err := Install(t.Context(), o); err != nil {
					t.Fatal(err)
				}
				now = now.Add(time.Hour)
			}
			if restarts != 0 || health != 0 || workers != 2 {
				t.Fatalf("busy installs: restarts=%d health=%d workers=%d", restarts, health, workers)
			}
			if !strings.Contains(output.String(), "Deferred engine restart: a voice call is live") {
				t.Fatal(output.String())
			}
			if _, err := os.Stat(filepath.Join(dir, Engine)); err != nil {
				t.Fatal("deferred install did not publish service files:", err)
			}
			unit, err := os.ReadFile(filepath.Join(dir, engineRestartWorker))
			if err != nil || !strings.Contains(string(unit), `ExecStart="/test/dyna" engine finish-restart --unit-dir "`+dir+`"`) {
				t.Fatalf("restart worker does not use the installed binary and unit directory: %s: %v", unit, err)
			}
			now = started.Add(time.Hour)
			if end == "reboot" {
				// A request made after startup must survive that startup's cleanup.
				if err := EngineRestarted(t.Context(), started.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if deadline, _ := engineRestartDeadline(t.Context(), dir, time.Time{}); deadline.IsZero() {
					t.Fatal("startup cleared a newer install request")
				}
				if err := EngineRestarted(t.Context(), now); err != nil {
					t.Fatal(err)
				}
				now = started.Add(3 * time.Hour)
				if err := Install(t.Context(), o); err != nil || restarts != 0 {
					t.Fatalf("old request cut short the new call: err=%v restarts=%d", err, restarts)
				}
			}
			if end == "next install" || end == "worker finished" {
				busy = false
				changed = end == "worker finished"
				if err := Install(t.Context(), o); err != nil {
					t.Fatal(err)
				}
			} else {
				pauses := 0
				o.Pause = func(_ context.Context, delay time.Duration) error {
					pauses++
					if delay <= 0 || delay > time.Second {
						t.Fatalf("invalid recheck delay: %v", delay)
					}
					if end != "deadline" {
						busy = false
					} else {
						now = started.Add(2 * time.Hour)
					}
					return nil
				}
				// A failed restart keeps the request so systemd can retry it.
				run := o.Run
				o.Run = func(ctx context.Context, args ...string) (string, error) {
					if strings.Contains(strings.Join(args, " "), "restart "+Engine) {
						return "", errors.New("manager unavailable")
					}
					return run(ctx, args...)
				}
				if err := WaitEngineRestart(t.Context(), o); err == nil {
					t.Fatal("lost the restart error")
				}
				o.Run = run
				if err := WaitEngineRestart(t.Context(), o); err != nil {
					t.Fatal(err)
				}
				if pauses != 1 {
					t.Fatalf("paused %d times; original deadline must survive repeated installs", pauses)
				}
			}
			if restarts != 1 {
				t.Fatalf("restarted %d times", restarts)
			}
			// The completed request must not cause another restart.
			if err := WaitEngineRestart(t.Context(), o); err != nil || restarts != 1 {
				t.Fatalf("completed request: err=%v restarts=%d", err, restarts)
			}
		})
	}
}
