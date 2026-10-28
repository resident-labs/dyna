package subagent

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
)

func TestMetadataReadsShareSnapshotAndSeeAtomicWrites(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	id := NewID()
	m := Meta{ID: id, Name: "first", RoomID: "room", Room: "room", ClosedPosts: map[string]bool{"post": true}, InheritedInboxes: []InheritedInbox{{PendingIDs: []string{"pending"}}}}
	write := func() {
		t.Helper()
		recordtest.Write(t, s.Root, id, m)
	}
	write()
	decoded := 0
	s.decodeMeta = func(b []byte, v any) error { decoded++; return json.Unmarshal(b, v) }
	got, err := s.ReadMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	got.ClosedPosts["post"] = false
	got.InheritedInboxes[0].PendingIDs[0] = "changed"
	if _, _, err := s.cachedMeta(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListMeta(); err != nil {
		t.Fatal(err)
	}
	s.QueueSignature(id)
	if decoded != 1 {
		t.Fatalf("unchanged reads decoded %d records, want 1", decoded)
	}
	got, err = s.ReadMeta(id)
	if err != nil || !got.ClosedPosts["post"] || got.InheritedInboxes[0].PendingIDs[0] != "pending" {
		t.Fatalf("returned value changed cache: %+v, %v", got, err)
	}
	// Two quick writes of the same size must still invalidate the snapshot,
	// including the session's display data.
	for _, name := range []string{"other", "final"} {
		m.Name = name
		write()
	}
	got, err = s.ReadMeta(id)
	if err != nil || got.Name != "final" {
		t.Fatalf("same-size writes: %+v, %v", got, err)
	}
	if decoded != 2 {
		t.Fatalf("changed read decoded %d records, want 2", decoded)
	}
	display, _, err := s.cachedMeta(id)
	if err != nil || display.Name != "final" {
		t.Fatalf("display read: %+v, %v", display, err)
	}
	recordtest.Remove(t, s.Root, id)
	if _, err := s.ReadMeta(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted metadata: %v", err)
	}
}

func TestClosedMetadataStaysCurrentAndShared(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	id := NewID()
	decoded := 0
	s.decodeMeta = func(b []byte, v any) error { decoded++; return json.Unmarshal(b, v) }
	recordtest.Write(t, s.Root, id, Meta{ID: id, RoomID: "room", Room: "room", SpawnBrief: "open snapshot"})
	if _, err := s.ReadMeta(id); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, s.Root, id, Meta{ID: id, RoomID: "room", Room: "room", Closed: time.Now(), SpawnBrief: "closed snapshot"})
	if _, err := s.ReadMeta(id); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadMeta(id)
	if err != nil || got.SpawnBrief != "closed snapshot" || decoded != 2 {
		t.Fatalf("closed snapshot = %+v, decodes %d, %v; want current metadata decoded once", got, decoded, err)
	}
	value, ok := infoCache.Load(s.recordKey(id))
	if !ok || value.(*infoEntry).meta.Closed.IsZero() {
		t.Fatal("an open snapshot survived the close")
	}
	if err := sessionlife.Delete(s.Root, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := infoCache.Load(s.recordKey(id)); ok {
		t.Fatal("production record deletion left the closed snapshot cached")
	}
}

func TestDeletionRacingSnapshotReadEvicts(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	id := NewID()
	recordtest.Write(t, s.Root, id, Meta{ID: id, RoomID: "room", Room: "room", Closed: time.Now()})
	decodeStarted, releaseDecode := make(chan struct{}), make(chan struct{})
	s.decodeMeta = func(b []byte, v any) error {
		close(decodeStarted)
		<-releaseDecode
		return json.Unmarshal(b, v)
	}
	afterDelete := sessionlife.AfterDelete
	t.Cleanup(func() { sessionlife.AfterDelete = afterDelete })
	deleteCommitted := make(chan struct{})
	sessionlife.AfterDelete = func(sessions, id string) {
		close(deleteCommitted)
		afterDelete(sessions, id)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := s.ReadMeta(id)
		readDone <- err
	}()
	<-decodeStarted
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- sessionlife.Delete(s.Root, id) }()
	<-deleteCommitted
	close(releaseDecode)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatal(err)
	}
	if _, ok := infoCache.Load(s.recordKey(id)); ok {
		t.Fatal("a read that started before deletion restored the deleted snapshot")
	}
}

func TestMetadataCacheUsesStoredServiceFlag(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	parent := Meta{ID: NewID(), RoomID: "room", Room: "room"}
	child := Meta{ID: NewID(), RoomID: "room", Room: "room", ParentSession: parent.ID}
	write := func(m Meta) {
		t.Helper()
		dir, _ := s.Dir(m.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := s.writeMeta(m); err != nil {
			t.Fatal(err)
		}
	}
	write(parent)
	write(child)
	before, err := s.ReadMeta(child.ID)
	if err != nil || before.Service {
		t.Fatalf("initial service: %+v, %v", before, err)
	}
	parent.Service = true
	write(parent)
	after, err := s.ReadMeta(child.ID)
	if err != nil || after.Service {
		t.Fatalf("changed ancestor: %+v, %v", after, err)
	}
}

func BenchmarkSessionPoll(b *testing.B) {
	root := os.Getenv("ROOM_SCAN_PROFILE_SESSIONS")
	if root == "" {
		b.Skip("set ROOM_SCAN_PROFILE_SESSIONS to a copied session store")
	}
	s := &Store{Root: root}
	sessions, err := s.ListMeta()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		for _, session := range sessions {
			if _, err := s.ReadMeta(session.ID); err != nil {
				b.Fatal(err)
			}
			s.QueueSignature(session.ID)
		}
	}
}

// The old reader ignored a malformed legacy-only done field. Keeping that
// tolerance must not make an otherwise readable session fail after caching.
func TestMetadataCacheIgnoresMalformedLegacyDone(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	id := NewID()
	recordtest.WriteRaw(t, s.Root, id, []byte(`{"id":"`+id+`","roomId":"room","room":"room","done":"unknown"}`))
	for range 2 {
		if _, err := s.ReadMeta(id); err != nil {
			t.Fatal(err)
		}
	}
}
