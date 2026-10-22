package watch

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// A stacked PR conflicts with the branch it targets, not with the repository
// default branch that it does not target.
func TestConflictNoticeNamesTheRecordsBaseBranch(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	r := e.track()
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].BaseRef = "stack/parent"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Mergeability = "CONFLICTING"
	e.write()
	// The local lane may fail in this fixture; the remote facts still apply.
	if _, err := e.s.refreshMergeFacts(context.Background()); err != nil {
		t.Logf("merge facts: %v", err)
	}
	for _, n := range e.sent {
		if strings.Contains(n.Text, "conflicts with stack/parent") {
			return
		}
	}
	t.Fatalf("conflict notices do not name the base branch: %+v", e.sent)
}
