package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/importer"
	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
)

// importMuseCredential is Muse's saved Meta API key in its native shape.
func importMuseCredential(key string) store.Credential {
	payload, _ := json.Marshal(map[string]any{"metaCredential": map[string]string{"api_key": key}})
	return store.Credential{Schema: "muse/auth-file@1", Payload: payload}
}

// importFixture saves one Muse key sign-in with a repository rule and builds a
// runtime that owns the same account store.
func importFixture(t *testing.T) (*store.Store, *Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	st := &store.Store{Root: filepath.Join(dir, "accounts")}
	var source string
	if err := st.Update(func(state *store.State) error {
		meta, err := state.Save("muse", "meta-muse", "meta:user:workspace", "Work", importMuseCredential("fixture-meta-key"))
		if err != nil {
			return err
		}
		source = meta.ID
		home := filepath.Join(st.Root, "muse", meta.ID)
		if err := os.MkdirAll(home, 0700); err != nil {
			return err
		}
		state.Find(meta.ID).NativeHome = home
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scopes, err := reposcope.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := scopes.Set(source, []string{"github.com/org/name"}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: &Accounts{Root: st.Root}}
	installFakePiAuth(t, r.Accounts)
	return st, r, source
}

// TestImportKeepsTheSourceWhenThePiWriteFails stops the Pi write at the file
// system and requires the source row and credential to stay as they were.
func TestImportKeepsTheSourceWhenThePiWriteFails(t *testing.T) {
	st, r, source := importFixture(t)
	state, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(state)
	// A file where Pi's account directory belongs makes the write fail.
	if err := os.WriteFile(filepath.Join(st.Root, "pi"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	im := &importer.Importer{Store: st, Host: PiImportHost{Runtime: r}}
	result, err := im.Run(context.Background(), importer.Request{Harness: "muse"})
	if err != nil {
		t.Fatalf("a per-row failure ended the import: %v", err)
	}
	if len(result.Refused) != 1 || result.Refused[0].Reason != importer.ReasonMoveFailed {
		t.Fatalf("refused = %+v", result.Refused)
	}
	after, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(after)
	if string(raw) != string(before) || after.Find(source) == nil {
		t.Fatal("a failed write changed the source")
	}
	accounts, _ := r.Accounts.List()
	for _, account := range accounts {
		if account.Harness == "pi" {
			t.Fatalf("a failed write left a Pi account: %+v", account)
		}
	}
}

// TestImportRemovesThePiAccountWhenItReadsBackNotReady covers the rollback that
// runs after the account exists: the lock must be free so the removal can take
// it, and no Pi copy of the source key may stay.
func TestImportRemovesThePiAccountWhenItReadsBackNotReady(t *testing.T) {
	st, r, source := importFixture(t)
	before, _ := st.Read()
	readBack := piImportConfirm
	var createdID string
	piImportConfirm = func(_ string, saved NativeAccount, _ importer.PiLogin) error {
		createdID = saved.ID
		return errors.New("Pi read the moved sign-in as not signed in.")
	}
	t.Cleanup(func() { piImportConfirm = readBack })
	im := &importer.Importer{Store: st, Host: PiImportHost{Runtime: r}}
	result, err := im.Run(context.Background(), importer.Request{Harness: "muse"})
	if err != nil {
		t.Fatalf("a per-row failure ended the import: %v", err)
	}
	if len(result.Refused) != 1 || result.Refused[0].Reason != importer.ReasonMoveFailed {
		t.Fatalf("refused = %+v", result.Refused)
	}
	accounts, err := r.Accounts.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.Harness == "pi" {
			t.Fatalf("the rollback left a Pi account: %+v", account)
		}
	}
	if createdID == "" {
		t.Fatal("the test never reached the read-back")
	}
	if _, err := os.Stat(filepath.Join(st.Root, "pi", createdID)); !os.IsNotExist(err) {
		t.Fatalf("the rollback left the Pi home %s", createdID)
	}
	after, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	rawBefore, _ := json.Marshal(before)
	rawAfter, _ := json.Marshal(after)
	if string(rawBefore) != string(rawAfter) || after.Find(source) == nil {
		t.Fatal("the rollback changed the source")
	}
}
