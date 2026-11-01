package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func pinStore(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func pinSession(t *testing.T, st *Store, id contract.SessionID) {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('room','room',0,0,0)"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES(?,'room','worker','test','active','',0)", id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A box restart replaces the box, not the session row, and the engine restarts with the
// same database. A pin lives in the store, so it outlasts both.
func TestSessionAccountPinSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	st := pinStore(t, dir)
	ctx := context.Background()
	pinSession(t, st, "session")
	if err := st.SaveProxyAuth(ctx, ProxyAuth{ID: "proxy", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "proxy", contract.ProviderAnthropic, "Personal"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionAccountPin(ctx, "session", "account"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = pinStore(t, dir)
	defer st.Close()
	if pin, err := st.SessionAccountPin(ctx, "session"); err != nil || pin != "account" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
	if proxyID, err := st.SessionPinnedProxyID(ctx, "session"); err != nil || proxyID != "proxy" {
		t.Fatalf("proxy = %q, %v", proxyID, err)
	}
}

func TestSessionAccountPinReplacesAndClears(t *testing.T) {
	st := pinStore(t, t.TempDir())
	defer st.Close()
	ctx := context.Background()
	pinSession(t, st, "session")
	for _, account := range []contract.AccountID{"first", "second"} {
		if err := st.SetSessionAccountPin(ctx, "session", account); err != nil {
			t.Fatal(err)
		}
	}
	if pin, err := st.SessionAccountPin(ctx, "session"); err != nil || pin != "second" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
	if err := st.SetSessionAccountPin(ctx, "session", ""); err != nil {
		t.Fatal(err)
	}
	if pin, err := st.SessionAccountPin(ctx, "session"); err != nil || pin != "" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
}

func TestPinsCountOnlyOpenSessions(t *testing.T) {
	st := pinStore(t, t.TempDir())
	defer st.Close()
	ctx := context.Background()
	pinSession(t, st, "session")
	if err := st.SetSessionAccountPin(ctx, "session", "account"); err != nil {
		t.Fatal(err)
	}
	if pins, err := st.PinnedAccounts(ctx); err != nil || len(pins) != 1 || pins[0] != "account" {
		t.Fatalf("pins = %v, %v", pins, err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET closed_at=1 WHERE id='session'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if pins, err := st.PinnedAccounts(ctx); err != nil || len(pins) != 0 {
		t.Fatalf("pins after close = %v, %v", pins, err)
	}
}

func TestRoomDeleteClearsPins(t *testing.T) {
	found := false
	for _, statement := range deleteRoom {
		if statement == "DELETE FROM session_accounts WHERE session IN (SELECT id FROM sessions WHERE room=?1)" {
			found = true
		}
	}
	if !found {
		t.Fatal("room delete would leave a row that blocks deleting its session")
	}
}
