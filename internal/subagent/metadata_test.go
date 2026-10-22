package subagent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
)

func TestListMetaDoesNotRecoverTurns(t *testing.T) {
	s := &Store{Root: t.TempDir(), ReportRepaired: func(*Store, string, int) { t.Fatal("metadata scan repaired a turn") }}
	id := "ses_20261017-120000-abcdef12"
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, s.Root, id, Meta{ID: id, Name: "orphan", RoomID: "global", Room: "global", Turns: 1})
	turn := []byte(`{"n":1,"status":"running"}`)
	path := filepath.Join(dir, "turns", "1.json")
	if err := os.WriteFile(path, turn, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMeta()
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("ListMeta = %+v, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(turn) {
		t.Fatalf("turn changed: %s, %v", after, err)
	}
}

func TestListMetaFailsOnUnreadableRecord(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	id := "ses_20261017-120000-abcdef12"
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListMeta(); err != nil || len(got) != 0 {
		t.Fatalf("missing record = %+v, %v", got, err)
	}
	recordtest.WriteRaw(t, s.Root, id, []byte("{"))
	if _, err := s.ListMeta(); err == nil || !strings.Contains(err.Error(), id+" (unreadable:") {
		t.Fatalf("corrupt record = %v", err)
	}
}

func TestListMetaSkipsUnassignedLegacyHistory(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	for _, closed := range []bool{false, true} {
		id := NewID()
		raw := []byte(`{"id":"` + id + `","name":"old","turns":1}`)
		if closed {
			raw = []byte(`{"id":"` + id + `","name":"old","turns":1,"closed":"2026-10-05T00:00:00Z"}`)
		}
		dir, _ := s.Dir(id)
		if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
			t.Fatal(err)
		}
		recordtest.WriteRaw(t, s.Root, id, raw)
		turn := Turn{N: 1, Status: TurnOK, Prompt: "old work"}
		if err := s.writeTurn(id, turn); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListMeta()
		if err != nil || len(got) != 0 {
			t.Fatalf("ListMeta = %+v, %v", got, err)
		}
		if _, err := s.ReadMeta(id); !errors.Is(err, ErrNoRoom) {
			t.Fatalf("legacy routing allowed: %v", err)
		}
		// History remains readable without assigning a room or changing files.
		meta, _, err := s.readMetaRecord(id)
		if err != nil || meta.ID != id {
			t.Fatalf("metadata lost: %+v, %v", meta, err)
		}
		saved, err := s.ReadTurn(id, 1)
		if err != nil || saved.Prompt != turn.Prompt {
			t.Fatalf("history lost: %+v, %v", saved, err)
		}
		after, err := os.ReadFile(recordtest.Path(t, s.Root, id))
		if err != nil || !bytes.Equal(raw, after) {
			t.Fatalf("legacy record rewritten: %s, %v", after, err)
		}
	}
}

// Retained sessions of the removed global room keep their global:<name>
// addresses.
func TestLegacyGlobalAddressesResolve(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	for _, meta := range []Meta{
		{ID: "ses_20261017-120000-abcdef12", Name: "lead", RoomID: "global", Room: "global"},
		{ID: "ses_20261017-120001-abcdef13", Name: "done", RoomID: "global", Room: "global", Closed: time.Now()},
	} {
		recordtest.Write(t, s.Root, meta.ID, meta)
	}
	if info, err := s.Resolve("global:lead"); err != nil || info.Name != "lead" {
		t.Fatalf("Resolve(global:lead) = %+v, %v", info, err)
	}
	if info, err := s.ResolveClosed("global:done"); err != nil || info.Name != "done" {
		t.Fatalf("ResolveClosed(global:done) = %+v, %v", info, err)
	}
}

func TestInternalRootAddressResolvesForReadCommands(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	for _, name := range []string{"work", "other"} {
		meta := Meta{ID: NewID(), Name: RoleRoot, Role: RoleRoot, RoomID: "room_" + name, Room: name, Turns: 1}
		if err := os.MkdirAll(filepath.Join(s.Root, meta.ID, "turns"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := s.writeMeta(meta); err != nil {
			t.Fatal(err)
		}
		if err := s.writeTurn(meta.ID, Turn{N: 1, Status: TurnOK, Prompt: "root history", Result: "root answer"}); err != nil {
			t.Fatal(err)
		}
		for _, resolve := range []func(string) (Info, error){s.Resolve, s.ResolveSnapshot} {
			info, err := resolve(meta.Address())
			if err != nil || info.ID != meta.ID {
				t.Fatalf("resolve %s = %+v, %v", meta.Address(), info, err)
			}
			turn, err := s.ReadTurn(info.ID, 1)
			if err != nil || turn.Result != "root answer" || turn.Prompt != "root history" {
				t.Fatalf("root turn = %+v, %v", turn, err)
			}
		}
	}
	if _, err := s.Resolve("root"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("bare root should be ambiguous across rooms: %v", err)
	}
}

func TestListSkipsClosedSessionTurns(t *testing.T) {
	s := &Store{Root: t.TempDir(), ReportRepaired: func(*Store, string, int) { t.Fatal("list read a closed session's turn") }}
	id := "ses_20261017-120000-abcdef12"
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, s.Root, id, Meta{ID: id, Name: "closed", RoomID: "global", Room: "global", Turns: 1, Closed: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), []byte(`{"n":1,"status":"running"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(true)
	if err != nil || len(got) != 1 || got[0].Status != StatusClosed || got[0].CurrentTurn != 0 {
		t.Fatalf("List = %+v, %v", got, err)
	}
}
