package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/websvc"
)

func TestAccountUpdateReplayAfterMaintenanceAndLostReply(t *testing.T) {
	db, w, dir := thingsStore(t)
	registry := &accountstore.Store{Root: filepath.Join(dir, "accounts")}
	if err := accountstore.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	var meta accountstore.Metadata
	if err := registry.Update(func(st *accountstore.State) error {
		var err error
		meta, err = st.Save("fixture", "", "identity", "original", accountstore.Credential{Schema: "fixture-v1", Payload: json.RawMessage(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	handlers := 0
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "ObserveFixtureQuota", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, delta int) error {
			handlers++
			err := registry.UpdateContext(ctx, func(st *accountstore.State) error {
				if handlers == 1 {
					return errors.New("callback rejected")
				}
				entry := st.Find(meta.ID)
				entry.Observation = &accountstore.Observation{Failures: delta}
				return nil
			})
			if err != nil {
				return err
			}
			// Stop after the mutation, before the dispatcher stores a reply.
			return errors.New("reply lost after commit")
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, w, t.Logf)
	enginetest.Serve(t, dir, calls)
	client := engineclient.New("")
	ctx := callspec.WithRequest(t.Context(), "ses_fixture/1/2/3/4")
	send := func(delta int) error {
		return client.Call(ctx, "ObserveFixtureQuota", []any{delta}, nil)
	}
	if err := send(1); err == nil || !strings.Contains(err.Error(), "callback rejected") {
		t.Fatalf("initial rejection = %v", err)
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("maintenance cached success before mutation: receipts=%d, err=%v", receipts, err)
	}
	if err := send(1); err == nil || !strings.Contains(err.Error(), "reply lost after commit") {
		t.Fatalf("commit before reply = %v", err)
	}
	var result []byte
	if err := db.QueryRow("SELECT result FROM request_ids").Scan(&result); err != nil || result != nil {
		t.Fatalf("missing mutation-only receipt: result=%s, err=%v", result, err)
	}
	if err := send(1); err != nil {
		t.Fatalf("cached mutation replay = %v", err)
	}
	if err := send(2); err == nil || !strings.Contains(err.Error(), "request ID was reused with different method or arguments") {
		t.Fatalf("changed observation received cached success: %v", err)
	}
	if handlers != 2 {
		t.Fatalf("handler ran %d times, want rejected and committed calls only", handlers)
	}
	st, err := registry.Read()
	if err != nil {
		t.Fatal(err)
	}
	entry := st.Find(meta.ID)
	if entry.Observation == nil || entry.Observation.Failures != 1 {
		t.Fatal("replay changed the committed observation")
	}
}
