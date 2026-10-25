package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/importer"
	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
)

// importCodexCredential is Codex's saved ChatGPT login in its native shape.
func importCodexCredential(exp int64) store.Credential {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	claims := struct {
		Exp int64 `json:"exp"`
	}{Exp: exp}
	access := encode(struct {
		Alg string `json:"alg"`
	}{Alg: "none"}) + "." + encode(claims) + ".signature"
	auth, _ := json.Marshal(struct {
		Tokens struct {
			Access    string `json:"access_token"`
			Refresh   string `json:"refresh_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}{Tokens: struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		AccountID string `json:"account_id"`
	}{Access: access, Refresh: "codex-refresh", AccountID: "account-1"}})
	payload, _ := json.Marshal(struct {
		Auth json.RawMessage `json:"auth.json"`
	}{Auth: auth})
	return store.Credential{Schema: "codex/auth-file@1", Payload: payload}
}

// importFixture saves one Codex sign-in with a repository rule and builds a
// runtime that owns the same account store.
func importFixture(t *testing.T) (*store.Store, *Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	st := &store.Store{Root: filepath.Join(dir, "accounts")}
	var source string
	if err := st.Update(func(state *store.State) error {
		meta, err := state.Save("codex", "chatgpt", "openai:user:workspace", "Work", importCodexCredential(1_800_000_000))
		if err != nil {
			return err
		}
		source = meta.ID
		home := filepath.Join(st.Root, "codex", meta.ID)
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
	return st, &Runtime{Accounts: &Accounts{Root: st.Root}}, source
}

// TestImportMovesACodexSignInIntoAPiAccount runs the real Pi write: the new
// account holds exactly the codex entry, reads back ready, keeps the repo
// rules, and only then does the source row retire.
func TestImportMovesACodexSignInIntoAPiAccount(t *testing.T) {
	st, r, source := importFixture(t)
	im := &importer.Importer{Store: st, Host: PiImportHost{Runtime: r}}
	result, err := im.Run(context.Background(), importer.Request{Harness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moved) != 1 || result.Moved[0].Account == "" {
		t.Fatalf("moved = %+v", result.Moved)
	}
	accounts, err := r.Accounts.List()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	account := accounts[0]
	if account.Provider != store.ProviderChatGPT || account.Kind != store.KindOAuth || account.Status != "ready" || account.ID != result.Moved[0].Account {
		t.Fatalf("account = %+v", account)
	}
	entries := piSnapshotEntries(account.Home)
	if len(entries) != 1 || entries["openai-codex"] == nil {
		t.Fatalf("home holds %v", entries)
	}
	var entry struct {
		Type      string `json:"type"`
		Access    string `json:"access"`
		Refresh   string `json:"refresh"`
		Expires   int64  `json:"expires"`
		AccountID string `json:"accountId"`
	}
	if json.Unmarshal(entries["openai-codex"], &entry) != nil {
		t.Fatal("the saved entry is not readable")
	}
	if entry.Type != "oauth" || entry.Access == "" || entry.Refresh != "codex-refresh" || entry.Expires != 1_800_000_000_000 || entry.AccountID != "account-1" {
		t.Fatalf("entry = %+v", entry)
	}
	state, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Find(source) != nil {
		t.Fatal("the source row was not retired")
	}
	scopes, err := reposcope.Open(filepath.Dir(st.Root))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := scopes.Rules()
	if err != nil || len(rules[account.ID]) != 1 || rules[account.ID][0] != "github.com/org/name" {
		t.Fatalf("rules = %v, %v", rules, err)
	}
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
	result, err := im.Run(context.Background(), importer.Request{Harness: "codex"})
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
// it, and no Pi copy of the refresh token may stay.
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
	result, err := im.Run(context.Background(), importer.Request{Harness: "codex"})
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
