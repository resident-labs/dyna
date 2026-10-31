package eval

import (
	"context"
	"path/filepath"
	"testing"
)

func TestJournalPreventsUnapprovedReplay(t *testing.T) {
	ctx := context.Background()
	j, err := OpenJournal(filepath.Join(t.TempDir(), "attempts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	cell := Cell{Task: 1, Provider: Opus, Arm: Off}
	if err := j.Begin(ctx, cell, 1); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(ctx, cell, 1); err == nil {
		t.Fatal("duplicate initial attempt allowed")
	}
	if err := j.Begin(ctx, cell, 2); err == nil {
		t.Fatal("retry before completion allowed")
	}
	if err := j.Finish(ctx, cell, 1, Attempt{ProviderFailure: true}); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(ctx, cell, 2); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(ctx, cell, 2); err == nil {
		t.Fatal("duplicate retry allowed")
	}
	other := Cell{Task: 2, Provider: Opus, Arm: Off}
	if err := j.Begin(ctx, other, 1); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(ctx, other, 1, Attempt{ProviderFailure: true, OutputStarted: true}); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(ctx, other, 2); err == nil {
		t.Fatal("retry after model output allowed")
	}
}
