package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
)

const testProviderKey = "test-key"

// fakeOpenCode answers the four commands the provider-key code runs. It keeps
// credentials in a file beside OPENCODE_DB and records every argument list.
const fakeOpenCode = `#!/bin/sh
db="$OPENCODE_DB.creds"; touch "$db"
echo "$*" >> "$OPENCODE_DB.argv"
case "$1 $2" in
"api GET") echo '{"data":[{"id":"zai","name":"Z.AI","methods":[{"type":"key"},{"type":"env"}]},{"id":"sso","name":"SSO","methods":[{"type":"oauth"}]}]}' ;;
"auth list") printf '['; sep=; while read p id; do printf '%s{"id":"%s","name":"Name-%s","connections":[{"type":"credential","id":"%s","method":"key"}]}' "$sep" "$p" "$p" "$id"; sep=,; done < "$db"; echo ']' ;;
"auth import") body=$(cat); echo "$body" > "$OPENCODE_DB.stdin"
  p=$(echo "$body" | sed 's/.*"integrationID":"\([^"]*\)".*/\1/'); id=$(echo "$body" | sed 's/.*"id":"\(cred_[^"]*\)".*/\1/'); echo "$p $id" >> "$db" ;;
"auth logout") grep -v "^$3 $4\$" "$db" > "$db.new"; mv "$db.new" "$db" ;;
*) exit 1 ;;
esac
`

func providerKeysRuntime(t *testing.T) *Runtime {
	t.Helper()
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil || args[0] == "curl" {
			return err
		}
		root := args[4]
		pkg, _ := bundlePackage("opencode")
		if cli := filepath.Join(root, "lib/node_modules", pkg, "cli.js"); fileExists(cli) {
			return fsutil.WriteExecutable(cli, []byte(fakeOpenCode), 0700)
		}
		return nil
	}
	// A private-runtime snapshot resolves the harness versions it lists. That
	// resolver reads vendor metadata over the network, which a test must not do:
	// the first fetch waits two seconds before it gives up.
	return &Runtime{Bundles: m, Accounts: testAccounts(t), privateRecommendations: func(context.Context) []RecommendedVersion { return []RecommendedVersion{} }}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// seedPiAccount stores one provider entry in a Pi account of its own.
func seedPiAccount(t *testing.T, r *Runtime, id, entry string) NativeAccount {
	t.Helper()
	account, err := r.Accounts.CreateProvider("pi", piProvider(id), store.KindOAuth, id+" login")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"`+id+`":`+entry+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	account, err = r.Accounts.Capture(context.Background(), account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Accounts.setActiveAccount(account); err != nil {
		t.Fatal(err)
	}
	return account
}

func TestPiAuthLockRenewsItsMtimeAndLosesToATakeover(t *testing.T) {
	beat := make(chan struct{})
	piBeat, piBeatDone = time.Millisecond, func() { beat <- struct{}{} }
	t.Cleanup(func() { piBeat, piBeatDone = 2*time.Second, nil })
	path := filepath.Join(t.TempDir(), "auth.json")
	lock, err := lockPiAuth(context.Background(), path)
	if err != nil || !lock.held() {
		t.Fatal("not held after locking", err)
	}
	// Backdated past Pi's stale limit, the lock is renewed by the next beat.
	// The renewal is concurrent, so wait for a beat that ran after the backdate.
	old := time.Now().Add(-2 * piAuthStale)
	lock.mu.Lock()
	err = os.Chtimes(path+".lock", old, old)
	lock.info, _ = os.Stat(path + ".lock")
	lock.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	<-beat
	<-beat
	if info, _ := os.Stat(path + ".lock"); time.Since(info.ModTime()) > piAuthStale || !lock.held() {
		t.Fatal("the lock was not renewed")
	}
	// Another process takes the lock over: it is no longer ours and stays.
	changed := time.Now().Add(-time.Hour)
	lock.mu.Lock()
	err = os.Chtimes(path+".lock", changed, changed)
	lock.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range beat {
		}
	}()
	if lock.held() {
		t.Fatal("a touched lock still counts as ours")
	}
	lock.release()
	if !fileExists(path + ".lock") {
		t.Fatal("removed a lock that is no longer ours")
	}
}

func TestPiAuthLockReportsAStaleLockItCannotRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Join(lock, "inner"), 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * piAuthStale)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := lockPiAuth(context.Background(), path); err == nil {
		t.Fatal("took a lock it could not clear")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := os.Chtimes(lock, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := lockPiAuth(cancelled, path); err == nil {
		t.Fatal("kept waiting after cancellation")
	}
}

func TestPiProviderKeyCreatesAndActivatesAnAccount(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0-dyna.3")
	ctx := context.Background()
	if _, found, err := r.piAccountForProvider(ctx, store.ProviderZAI); err != nil || found {
		t.Fatalf("fresh install already has a zai account: %+v %v", found, err)
	}
	if _, err := r.AddProviderKey(ctx, "pi", store.ProviderZAI, "", testProviderKey); err != nil {
		t.Fatal(err)
	}
	account, found, err := r.piAccountForProvider(ctx, store.ProviderZAI)
	if err != nil || !found || !account.Active || account.Status != "ready" {
		t.Fatalf("account after adding a key: %+v %v", account, err)
	}
	raw, _ := os.ReadFile(filepath.Join(account.Home, "auth.json"))
	if !strings.Contains(string(raw), `"api_key"`) {
		t.Fatalf("account login file: %s", raw)
	}
}

func TestPiAuthWriteKeepsPisFileWhenTheLockIsLostBeforeCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockPiAuth(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	// Pi takes the lock over and updates its file while Dyna's temporary file is being written.
	check := func() error {
		changed := time.Now().Add(-time.Hour)
		if err := os.Chtimes(path+".lock", changed, changed); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"pi":"update"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if lock.held() {
			return nil
		}
		return errors.New("lost")
	}
	if err := writePrivateAccountJSONChecked(path, map[string]string{"dyna": "old"}, check); err == nil {
		t.Fatal("committed with a lost lock")
	}
	if raw, _ := os.ReadFile(path); string(raw) != `{"pi":"update"}` {
		t.Fatalf("Pi's update was overwritten: %s", raw)
	}
}

func TestPiAuthRestoreKeepsPisFileWhenTheLockIsLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"edited":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Pi takes over the lock and updates the file just before the restore commits.
	takeover := func() {
		changed := time.Now().Add(-time.Hour)
		os.Chtimes(path+".lock", changed, changed)
		os.WriteFile(path, []byte(`{"pi":"update"}`), 0600)
	}
	if err := restorePiAuth(path, []byte(`{"saved":1}`), true, takeover); err == nil {
		t.Fatal("restored over Pi's update")
	}
	if raw, _ := os.ReadFile(path); string(raw) != `{"pi":"update"}` {
		t.Fatalf("Pi's update was overwritten: %s", raw)
	}
	// A planted symlink and a permissive file at the old fixed name are never touched.
	victim := filepath.Join(filepath.Dir(path), "victim")
	os.WriteFile(victim, []byte("keep"), 0644)
	os.Symlink(victim, path+".recovery")
	os.RemoveAll(path + ".lock")
	os.WriteFile(path, []byte(`{"edited":1}`), 0600)
	if err := restorePiAuth(path, []byte(`{"saved":2}`), true, takeover); err == nil {
		t.Fatal("restored over Pi's update")
	}
	found, _ := filepath.Glob(path + ".recovery-*")
	if len(found) != 2 {
		t.Fatalf("two failed restores must keep two snapshots: %v", found)
	}
	seen := map[string]bool{}
	for _, f := range found {
		raw, _ := os.ReadFile(f)
		info, _ := os.Stat(f)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("recovery mode %v", info.Mode())
		}
		seen[string(raw)] = true
	}
	if !seen[`{"saved":1}`] || !seen[`{"saved":2}`] {
		t.Fatalf("snapshots: %v", seen)
	}
	if raw, _ := os.ReadFile(victim); string(raw) != "keep" {
		t.Fatal("wrote through a planted symlink")
	}
	os.RemoveAll(path + ".lock")
	// A file that did not exist is removed, and an ordinary restore succeeds.
	if err := restorePiAuth(path, []byte(`{"saved":1}`), true, nil); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != `{"saved":1}` {
		t.Fatalf("restore: %s", raw)
	}
	if err := restorePiAuth(path, nil, false, nil); err != nil || fileExists(path) {
		t.Fatal("did not remove the new file", err)
	}
}
