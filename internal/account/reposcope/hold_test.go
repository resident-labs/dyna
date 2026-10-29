package reposcope

import (
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// A hold survives a restart with its backoff level, and clearing it removes both.
func TestHoldSurvivesRestartAndClears(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	want := Hold{Until: now.Add(4 * time.Hour), Level: 2}
	if err := s.Hold("acct", want); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	got, err := reopened.LimitOf("acct")
	if err != nil {
		t.Fatal(err)
	}
	if got.Until.Unix() != want.Until.Unix() || got.Level != want.Level || got.Stated {
		t.Fatalf("LimitOf after restart = %+v, want until %d level %d unstated", got, want.Until.Unix(), want.Level)
	}
	if err := reopened.ClearLimit("acct"); err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.LimitOf("acct"); err != nil || !got.Until.IsZero() || got.Level != 0 {
		t.Fatalf("LimitOf after ClearLimit = %+v, %v; want no hold", got, err)
	}
}

// A shorter end never replaces a later one, and its level goes with the later end.
func TestHoldKeepsTheLevelOfTheLaterEnd(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	now := time.Unix(1_700_000_000, 0)
	if err := s.Hold("acct", Hold{Until: now.Add(5 * time.Hour), Level: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.Hold("acct", Hold{Until: now.Add(time.Hour), Level: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LimitOf("acct")
	if err != nil {
		t.Fatal(err)
	}
	if got.Until.Unix() != now.Add(5*time.Hour).Unix() || got.Level != 3 {
		t.Fatalf("LimitOf = %+v, want the later end at level 3", got)
	}
}

// A successful probe or refresh clears a no-timer hold, but never a timed one.
func TestClearOpenEndedKeepsTimedHolds(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	now := time.Unix(1_700_000_000, 0)
	if err := s.Hold("timed", Hold{Until: now.Add(time.Hour), Level: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Hold("open", Hold{Until: OpenEnded}); err != nil {
		t.Fatal(err)
	}
	if err := s.Hold("stated", Hold{Until: now.Add(2 * time.Hour), Stated: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"timed", "open", "stated"} {
		if err := s.ClearOpenEnded(id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.LimitOf("open"); !got.Until.IsZero() {
		t.Fatalf("open hold after ClearOpenEnded = %+v, want cleared", got)
	}
	for _, id := range []string{"timed", "stated"} {
		if got, _ := s.LimitOf(id); got.Until.IsZero() {
			t.Fatalf("%s hold after ClearOpenEnded = %+v, want kept", id, got)
		}
	}
}

// Two failures that both compute the same next hold leave one level step, not two.
func TestHoldIsIdempotentForConcurrentFailures(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	next := Hold{Until: time.Unix(1_700_003_600, 0), Level: 1}
	for range 2 {
		if err := s.Hold("acct", next); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LimitOf("acct")
	if err != nil {
		t.Fatal(err)
	}
	if got.Level != 1 {
		t.Fatalf("LimitOf level = %d, want 1 after two identical failures", got.Level)
	}
}
