package shells

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store/storetest"
)

func TestWorkflowHandoffWaitsForShellAndUsesNewOwner(t *testing.T) {
	data := storetest.Isolate(t)
	from, to := "ses_20261026-000001-aaaaaaaa", "ses_20261026-000002-aaaaaaaa"
	sessions := filepath.Join(data, "sessions")
	for _, id := range []string{from, to} {
		if err := os.MkdirAll(filepath.Join(sessions, id), 0700); err != nil {
			t.Fatal(err)
		}
		if err := sessionlife.Save(sessions, id, struct {
			ID string `json:"id"`
		}{id}); err != nil {
			t.Fatal(err)
		}
	}
	id := "wf_20261026-010101-abcdef01"
	meta := runstore.Meta{ID: id, Status: "running", Detached: true, Session: from, ParentSession: from, StartedAt: time.Now()}
	runstoretest.Write(t, data, meta)
	s := DefaultStore()
	noTrees := func(context.Context, *sql.Conn, string, string) error { return nil }
	if _, err := s.HandoffRunOwner(context.Background(), id, from, to, noTrees); !errors.Is(err, runstore.ErrHandoffUnsafe) {
		t.Fatalf("old coordinator: %v", err)
	}
	meta.OwnerHandoff = true
	runstoretest.Write(t, data, meta)
	job, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), Owner: from, Run: id, WorkflowOwner: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.HandoffRunOwner(context.Background(), id, from, to, noTrees); !errors.Is(err, runstore.ErrHandoffUnsafe) {
		t.Fatalf("active step: %v", err)
	}
	if err := s.saveExit(job, Exit{Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	moved, err := s.HandoffRunOwner(context.Background(), id, from, to, noTrees)
	if err != nil || !moved {
		t.Fatalf("handoff = %v, %v", moved, err)
	}
	next, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), Owner: from, Run: id, WorkflowOwner: true})
	if err != nil || next.Owner != to {
		t.Fatalf("next step owner = %+v, %v", next, err)
	}
}
