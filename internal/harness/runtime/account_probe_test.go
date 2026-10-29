package runtime

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
)

// A probe must run on the account whose stated limit ends first. An unstated
// hold and a later stated one do not win.
func TestStatedHoldAccountIsTheStatedHoldThatEndsFirst(t *testing.T) {
	a := testAccounts(t)
	unstated := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	held := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	later := signedClaude(t, a, "other", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.MarkLimited(unstated.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(held.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(later.ID, now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, now)
	if err != nil || !ok || got.ID != held.ID {
		t.Fatalf("StatedHoldAccount = %s, %v, %v; want %s", got.ID, ok, err, held.ID)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, now.Add(4*time.Hour)); err != nil || !ok {
		t.Fatalf("after the first end, the later stated hold should remain: %v, %v", ok, err)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, now.Add(6*time.Hour)); err != nil || ok {
		t.Fatalf("after every end, no account should be held: %v, %v", ok, err)
	}
}
