package watch

import (
	"testing"
)

func TestRerunPassReplacesFailedAttemptWithoutPullUpdate(t *testing.T) {
	e := newEnv(t)
	e.setCI("failure", 1)
	// A fixed update time reaches the unchanged-PR shortcut; a rerun leaves it alone.
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.write()
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" {
		t.Fatalf("%+v", r)
	}
	e.tick()
	e.setCI("success", 2)
	e.tick()
	if r := e.record(); r.CI != "passed" {
		t.Fatalf("%+v", r)
	}
}
