package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
)

func TestPrivateProofSurvivesRetainedFileRemoval(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("muse", "saved")
	if err != nil {
		t.Fatal(err)
	}
	login := fixtureLogin("wanted")
	if err := writeAccountLogin(n.Home, login); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateAccountOrigin(n.Home, login.Identity); err != nil {
		t.Fatal(err)
	}
	if err := a.transaction(func(s *accountstore.State) error {
		e := s.Find(n.ID)
		e.Identity, e.NeedsLogin = login.Identity, false
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	data := filepath.Dir(a.Root)
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow("SELECT metadata FROM accounts WHERE id = ?", n.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "data-alias")
	if err := os.Symlink(data, alias); err != nil {
		t.Fatal(err)
	}
	fields["nativeHome"], err = json.Marshal(filepath.Join(alias, "accounts", "muse", n.ID))
	if err != nil {
		t.Fatal(err)
	}
	fields["futureField"] = json.RawMessage(`{"keep":9007199254740993}`)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE accounts SET metadata = ? WHERE id = ?", raw, n.ID); err != nil {
		t.Fatal(err)
	}
	// Invalid evidence must abort without stamping the migration complete.
	if err := writePrivateAccountOrigin(n.Home, "other"); err != nil {
		t.Fatal(err)
	}
	if err := MigratePrivateHomes(alias); err == nil {
		t.Fatal("accepted proof for another identity")
	}
	if err := writePrivateAccountOrigin(n.Home, login.Identity); err != nil {
		t.Fatal(err)
	}
	if err := MigratePrivateHomes(alias); err != nil {
		t.Fatal(err)
	}
	// Canonical SQL proof must win even when retained evidence becomes invalid.
	if err := os.WriteFile(filepath.Join(n.Home, ".private-login.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT metadata FROM accounts WHERE id = ?", n.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if patched, err := importPrivateProof(data, n.ID, raw); err != nil || patched != nil {
		t.Fatal("canonical proof did not win", err)
	}
	if err := MigratePrivateHomes(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(n.Home, ".private-login.json")); err != nil {
		t.Fatal(err)
	}
	found, err := a.Find(n.ID)
	if err != nil || found.Status != "ready" {
		t.Fatalf("account lost its private proof: %+v, %v", found, err)
	}
	if err := db.QueryRow("SELECT metadata FROM accounts WHERE id = ?", n.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["futureField"]) != `{"keep":9007199254740993}` {
		t.Fatal("unknown metadata changed")
	}
	if err := a.transaction(func(s *accountstore.State) error { s.Find(n.ID).Identity = "other"; return nil }, true); err != nil {
		t.Fatal(err)
	}
	found, err = a.Find(n.ID)
	if err != nil || found.Status != "not signed in" || requirePrivateSnapshot(found) == nil {
		t.Fatal("another identity reused private proof", found, err)
	}
}

func writePrivateAccountOrigin(home, identity string) error {
	path := filepath.Join(home, ".private-login.json")
	if err := checkPrivateAccountFile(path, false); err != nil {
		return err
	}
	raw, err := json.Marshal(privateAccountOrigin{1, identity, "private-home-v1"})
	if err != nil {
		return err
	}
	return writePrivateAccountJSON(path, native.Captured{Identity: identity, Credential: accountstore.Credential{Schema: "dyna/private-home@1", Payload: raw}})
}

type privateAccountOrigin struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
	Scope    string `json:"scope"`
}

func privateAccountProven(home, identity string) error {
	return verifyPrivateOrigin(filepath.Join(home, ".private-login.json"), identity)
}
