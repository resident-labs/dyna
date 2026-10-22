package maintenance

import (
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"testing"
	"time"
)

func TestLegacyDormantScratchOwnerStaysOpen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "ses_20261019-000001-aaaaaaaa"
	recordtest.Write(t, runstore.SessionsDir(), id, map[string]any{"id": id, "closed": time.Now(), "done": true})
	if err := sessionlife.Check(runstore.SessionsDir(), id); err != nil {
		t.Fatal(err)
	}
	got := scratchOwner(id)
	if !got.Known || !got.Open || !got.ClosedAt.IsZero() {
		t.Fatalf("legacy dormant scratch owner: %+v", got)
	}
}
