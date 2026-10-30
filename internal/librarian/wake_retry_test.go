package librarian

import (
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/settings"
)

// A deferred wake that fails keeps its work: it retries after RetryAfter, and
// the first retry that succeeds runs the pending pass.
func TestFailedDeferredWakeRetriesAndRunsThePass(t *testing.T) {
	h := setup(t)
	var deferred []deferral
	h.svc.Defer = func(repo string, at time.Time, wake func()) {
		deferred = append(deferred, deferral{repo: repo, at: at, wake: wake})
	}
	// The settings read can fail, as a transient error does. The flag is shared
	// by every copy of the service that a deferral keeps.
	load := h.svc.Settings
	failing := false
	h.svc.Settings = func() (settings.Values, error) {
		if failing {
			return settings.Values{}, errors.New("settings unreadable")
		}
		return load()
	}
	h.wake(t, "started")
	commitFile(t, h.repo, "web/app.ts", "x")
	h.wake(t, "batched")
	batched := deferred[len(deferred)-1]

	failing = true
	h.clock = batched.at
	batched.wake()
	retry := deferred[len(deferred)-1]
	if want := h.clock.Add(RetryAfter); !retry.at.Equal(want) {
		t.Fatalf("retry at %v, want %v", retry.at, want)
	}

	failing = false
	tasks := len(h.sessions.tasks)
	h.clock = retry.at
	retry.wake()
	if len(h.sessions.tasks) != tasks+1 {
		t.Fatalf("retried wake started no pass: tasks %d -> %d", tasks, len(h.sessions.tasks))
	}
}
