package harness

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	harnessruntime "dyna-agent/internal/harness/runtime"
)

// A stated hold ends only when a held-account probe succeeds. A turn on another
// model does not prove the provider's limit ended, so it keeps the hold.
func TestOrdinarySuccessKeepsAStatedHoldUntilAHeldProbe(t *testing.T) {
	rt := harnessruntime.New()
	s, err := reposcope.Open(filepath.Dir(rt.Accounts.Root))
	if err != nil {
		t.Fatal(err)
	}
	const id = "acct-stated-hold"
	if err := s.MarkReset(id, time.Now().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	clearAccountHold(Result{AccountID: id}, time.Now())
	if hold, _ := rt.Accounts.LimitOf(id); !hold.Stated || hold.Until.IsZero() {
		t.Fatalf("a turn on another model cleared the stated hold: %+v", hold)
	}
	clearAccountHold(Result{AccountID: id, HeldAccountProbed: true}, time.Now())
	if hold, _ := rt.Accounts.LimitOf(id); !hold.Until.IsZero() {
		t.Fatalf("a held-account probe kept the stated hold: %+v", hold)
	}
}
