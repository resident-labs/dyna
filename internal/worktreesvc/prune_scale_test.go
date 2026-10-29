package worktreesvc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/worktrees"
)

func pruneAllocsPerRecord(t *testing.T, count int) float64 {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for i := range count {
		id := fmt.Sprintf("open-owner-%02d", i)
		path := filepath.Join(os.TempDir(), fmt.Sprintf("dyna-wt-prune-cost-%02d", i))
		recordtest.Write(t, runstore.SessionsDir(), id, struct {
			ID  string `json:"id"`
			Dir string `json:"dir"`
		}{ID: id, Dir: path})
		if err := worktrees.Track(worktrees.Record{Path: path, Session: id, Port: 20000 + 10*i}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	allocs := testing.AllocsPerRun(2, func() {
		result, err := Prune(ctx, 0, false, true)
		if err != nil || result.Failure != "" || len(result.Decisions) != count {
			t.Fatalf("Prune() = %+v, %v; want %d decisions", result, err, count)
		}
	})
	return allocs / float64(count)
}

// Prune must share one ownership pass across its records. Repeating the full
// session and workflow scan for every record makes dry runs exceed the RPC limit.
func TestPruneOwnershipCostPerRecordDoesNotGrow(t *testing.T) {
	one := pruneAllocsPerRecord(t, 1)
	many := pruneAllocsPerRecord(t, 12)
	t.Logf("prune allocations per record: one %.0f, twelve %.0f", one, many)
	if many > 2*one {
		t.Fatalf("prune allocations per record grew from %.0f to %.0f; want at most %.0f", one, many, 2*one)
	}
}
