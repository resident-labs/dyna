package webpush

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestSubscriptionLivesInSQLite(t *testing.T) {
	s, dir := openStore(t)
	_, _, sub := newReceiver(t)
	if err := s.Subscribe("phone", sub, true); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	var endpoint string
	if err := db.QueryRow("SELECT endpoint FROM push_subscriptions WHERE device_id=?", "phone").Scan(&endpoint); err != nil {
		t.Fatalf("subscription has no SQLite row: %v", err)
	}
	if endpoint != sub.Endpoint {
		t.Fatalf("endpoint %q", endpoint)
	}
	if err := os.Remove(filepath.Join(dir, "connect", "push", subsFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	reloaded, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entry, ok := mustGet(t, reloaded, "phone"); !ok || !entry.Names {
		t.Fatalf("row lost after deleting legacy copy: %+v, %v", entry, ok)
	}
}

func writeOldSubscriptions(t *testing.T, dir string, entries []Entry) {
	t.Helper()
	path := filepath.Join(dir, "connect", "push", subsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteFile(path, data, 0600, false); err != nil {
		t.Fatal(err)
	}
}

func TestLegacySubscriptionsImportOnce(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	_, _, sub := newReceiver(t)
	old := Entry{DeviceID: "phone", Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth,
		Names: true, Created: time.Date(2026, 10, 1, 2, 3, 4, 0, time.UTC)}
	writeOldSubscriptions(t, dir, []Entry{old})
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := mustGet(t, s, "phone"); !ok || got != old {
		t.Fatalf("import: %+v, %v", got, ok)
	}
	if err := s.SetNames("phone", false); err != nil {
		t.Fatal(err)
	}
	before := state.For(s.db).Health().Writes
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if got := state.For(s.db).Health().Writes; got != before {
		t.Fatalf("own copy was imported: writes %d -> %d", before, got)
	}
	if got, _ := mustGet(t, s, "phone"); got.Names {
		t.Fatal("own copy replaced the row")
	}
	// Retained file replacements cannot change canonical subscriptions.
	old.DeviceID = "tablet"
	writeOldSubscriptions(t, dir, []Entry{old})
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustGet(t, s, "phone"); !ok {
		t.Fatal("retained file replaced canonical row")
	}
	if got, ok := mustGet(t, s, "tablet"); ok {
		t.Fatalf("retained file was imported again: %+v", got)
	}
	before = state.For(s.db).Health().Writes
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if got := state.For(s.db).Health().Writes; got != before {
		t.Fatalf("old binary change imported twice: %d -> %d", before, got)
	}
}

func TestCrashBeforeRollbackCopyKeepsCommittedSubscription(t *testing.T) {
	s, dir := openStore(t)
	_, _, sub := newReceiver(t)
	if err := s.Subscribe("phone", sub, false); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after commit, before the new rollback copy is written.
	if err := state.For(s.db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "UPDATE push_subscriptions SET names=1 WHERE device_id='phone'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := mustGet(t, s, "phone"); !got.Names {
		t.Fatal("stale rollback copy replaced the committed row")
	}
}

func TestSubscriptionHandlesShareRowsAndWritePublishesNothing(t *testing.T) {
	first, dir := openStore(t)
	second, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	state.For(first.db).SetPublish(func(hub.Event) { events++ })
	_, _, sub := newReceiver(t)
	if err := first.Subscribe("phone", sub, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustGet(t, second, "phone"); !ok {
		t.Fatal("second handle did not see first handle's write")
	}
	if err := second.Subscribe("tablet", sub, true); err != nil {
		t.Fatal(err)
	}
	if len(mustList(t, first)) != 2 {
		t.Fatal("second handle lost an existing row")
	}
	// Missing device errors must not publish either.
	if err := first.SetNames("missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing device: %v", err)
	}
	if events != 0 {
		t.Fatalf("web push has no event consumer, but published %d events", events)
	}
	if _, err := os.Stat(filepath.Join(dir, "connect", "push", subsFile)); !os.IsNotExist(err) {
		t.Fatalf("subscription write created a retained file: %v", err)
	}

}

func TestPruneCallbackRunsBeforeWriteAndPreservesNewSubscribe(t *testing.T) {
	s, _ := openStore(t)
	_, _, sub := newReceiver(t)
	if err := s.Subscribe("phone", sub, false); err != nil {
		t.Fatal(err)
	}
	sub.Endpoint = "https://web.push.apple.com/new"
	removed, err := s.RemoveDevicesNotIn(func(string) bool {
		if err := s.Subscribe("phone", sub, true); err != nil {
			t.Fatal(err)
		}
		return false
	})
	if err != nil || removed != 0 {
		t.Fatalf("prune deleted new subscribe: removed=%d, err=%v", removed, err)
	}
	if got, ok := mustGet(t, s, "phone"); !ok || got.Endpoint != sub.Endpoint {
		t.Fatalf("new subscribe lost: %+v", got)
	}
}

func TestSubscriptionWritesReportDatabaseFailure(t *testing.T) {
	s, dir := openStore(t)
	_, _, sub := newReceiver(t)
	events := 0
	state.For(s.db).SetPublish(func(hub.Event) { events++ })
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe("phone", sub, false); err == nil {
		t.Fatal("closed database write succeeded")
	}
	if _, _, err := s.Get("phone"); err == nil {
		t.Fatal("closed database read hid the error")
	}
	if events != 0 {
		t.Fatalf("failed write published %d events", events)
	}
}

func TestDamagedLegacyFileLeavesRowsUnchanged(t *testing.T) {
	s, dir := openStore(t)
	_, _, sub := newReceiver(t)
	if err := s.Subscribe("phone", sub, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "connect", "push", subsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteFile(path, []byte("broken"), 0600, false); err != nil {
		t.Fatal(err)
	}
	// The damaged file is skipped once (state.ErrSkipForeign): start-up and
	// later writes go on, and the rows stay.
	if err := Migrate(dir); err != nil {
		t.Fatalf("a damaged legacy file stopped the start: %v", err)
	}
	if got, ok := mustGet(t, s, "phone"); !ok || !got.Names {
		t.Fatalf("damaged import changed the row: %+v", got)
	}
	if err := s.Subscribe("laptop", sub, false); err != nil {
		t.Fatalf("a damaged legacy file blocked a later write: %v", err)
	}
	if _, ok := mustGet(t, s, "phone"); !ok {
		t.Fatal("the write after the skip lost a row")
	}
}

func TestOpenSubscriptionStoreDoesNotMigrateOutsideEngine(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	database.DisownSchema()
	defer database.OwnSchema()
	if _, err := OpenStore(dir); err == nil || !strings.Contains(err.Error(), "only the engine") {
		t.Fatalf("store open outside engine: %v", err)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='push_subscriptions'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("store open created %d tables: %v", tables, err)
	}
}

// A subscribe ignores retained files and preserves current subscriptions.
func TestSubscribeIgnoresRetainedFile(t *testing.T) {
	s, dir := openStore(t)
	_, _, sub := newReceiver(t)
	if err := s.Subscribe("phone", sub, true); err != nil {
		t.Fatal(err)
	}
	writeOldSubscriptions(t, dir, []Entry{{DeviceID: "tablet", Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth, Created: time.Now().UTC()}})
	if err := s.Subscribe("laptop", sub, false); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range mustList(t, s) {
		ids = append(ids, e.DeviceID)
	}
	if !reflect.DeepEqual(ids, []string{"laptop", "phone"}) {
		t.Fatalf("devices after the old binary's write and a subscribe = %v, want [laptop phone]", ids)
	}
}

// The stamp that #1483 kept in meta moves to state_mirrors, so a stale copy
// that the old stamp still covers is not imported over the rows.
func TestSubscriptionStampMovesFromMeta(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "webpush.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, _, sub := newReceiver(t)
	writeOldSubscriptions(t, dir, []Entry{{DeviceID: "stale", Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth, Created: time.Now().UTC()}})
	info, err := os.Stat(filepath.Join(dir, "connect", "push", subsFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key, value) VALUES('webpush.mirror', ?)", store.VersionOf(info).Key()); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entries := mustList(t, s); len(entries) != 0 {
		t.Fatalf("the stale copy was imported: %+v", entries)
	}
}
