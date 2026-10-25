package harness

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/harness/fakeworker"
)

// A worker can write its journal entry and finish between two polls. The
// completion check reads the journal once more before it decides, so that
// worker is never asked to write the entry it already wrote.
func TestJournalCompletionCheckSeesAnEntryWrittenBeforeTheResult(t *testing.T) {
	journalPath := createHarnessJournal(t, 41)
	p := fakeworker.InstallClaude(t, `printf '%s\n' '{"ts":1,"kind":"verification","message":"wrote the entry before the result","source":"agent"}' >> "$JOURNAL"; printf done`)
	p.Name = "journaled"
	p.Env = mergedEnv(p.Env, map[string]string{"JOURNAL": journalPath})
	var nudges []JournalNudge
	result, err := RunWithJournal(context.Background(), p, "task", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: time.Hour,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	})
	if err != nil || result.Output != "done" {
		t.Fatalf("turn = %#v, %v", result, err)
	}
	for _, nudge := range nudges {
		if nudge.Reason == JournalNudgeMissing {
			t.Fatalf("the worker that journaled was asked again: %#v", nudges)
		}
	}
	if result.JournalNudges != 0 {
		t.Fatalf("journal nudges = %d, want none", result.JournalNudges)
	}
}
