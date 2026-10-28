package runsvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestTransferRunToOpenSession(t *testing.T) {
	data := storetest.Isolate(t)
	from, to := "ses_20261026-000001-aaaaaaaa", "ses_20261026-000002-aaaaaaaa"
	root := subagent.DefaultStore().Root
	for _, id := range []string{from, to} {
		if err := os.MkdirAll(filepath.Join(root, id), 0700); err != nil {
			t.Fatal(err)
		}
		if err := sessionlife.Save(root, id, subagent.Meta{ID: id, Name: id, RoomID: "room_work", Room: "work", Depth: 1}); err != nil {
			t.Fatal(err)
		}
	}
	id := "wf_20261026-010101-abcdef01"
	runstoretest.Write(t, data, runstore.Meta{ID: id, Status: "running", OwnerHandoff: true, Detached: true, RoomID: "room_work", Room: "work", Session: from, ParentSession: from, StartedAt: time.Now()})
	actor := func(id string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: id, Room: "room_work"})
	}
	if err := Transfer(actor(to), id, to); err == nil {
		t.Fatal("unrelated session transferred the run")
	}
	if err := Transfer(actor(from), id, to); err != nil {
		t.Fatal(err)
	}
	got, err := runstore.ReadMeta(id)
	if err != nil || got.Status != "running" || got.ParentSession != to || got.Session != to {
		t.Fatalf("run = %+v, %v", got, err)
	}
	if err := Transfer(actor(from), id, from); err == nil {
		t.Fatal("former owner transferred the run")
	}
}
