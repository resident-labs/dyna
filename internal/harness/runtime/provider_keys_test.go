package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
		// A fake Pi catalog and a fake OpenCode executable, in npm's layout.
		catalog := filepath.Join(root, piCatalog)
		if err := os.MkdirAll(catalog, 0700); err != nil {
			return err
		}
		for _, name := range []string{"zai.json", "openrouter.json", ".manifest.json"} {
			if err := os.WriteFile(filepath.Join(catalog, name), []byte("{}"), 0600); err != nil {
				return err
			}
		}
		pkg, _ := bundlePackage("opencode")
		if cli := filepath.Join(root, "lib/node_modules", pkg, "cli.js"); fileExists(cli) {
			return fsutil.WriteExecutable(cli, []byte(fakeOpenCode), 0700)
		}
		return nil
	}
	return &Runtime{Bundles: m, Accounts: testAccounts(t)}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestPiProviderKeys(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	got, err := r.HarnessProviderKeys(ctx, "pi", "choices", "", "")
	if err != nil || len(got.Choices) != 2 || got.Choices[0].ID != "openrouter" {
		t.Fatalf("choices: %+v %v", got, err)
	}
	home, _ := r.Accounts.HarnessHome("pi")
	path := filepath.Join(home, "auth.json")
	// A provider sign-in is one more row of Pi's login, next to the API keys.
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"xai":{"type":"oauth","access":"keep"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey)
	if err != nil || len(got.Providers) != 2 || got.Providers[0].ID != "xai" || got.Providers[0].Auth != "oauth" || got.Providers[1].Auth != "api_key" {
		t.Fatalf("add: %+v %v", got, err)
	}
	raw, _ := os.ReadFile(path)
	var file map[string]map[string]string
	if json.Unmarshal(raw, &file) != nil || file["zai"]["type"] != "api_key" || file["zai"]["key"] != testProviderKey || file["xai"]["access"] != "keep" {
		t.Fatalf("auth.json: %s", raw)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if out, _ := json.Marshal(got); strings.Contains(string(out), testProviderKey) {
		t.Fatal("key in result")
	}
	got, err = r.HarnessProviderKeys(ctx, "pi", "remove", "zai", "")
	if err != nil || len(got.Providers) != 1 {
		t.Fatalf("remove: %+v %v", got, err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "zai") || !strings.Contains(string(raw), "xai") {
		t.Fatalf("auth.json after remove: %s", raw)
	}
}

func TestProviderKeysRejectBadInput(t *testing.T) {
	r := providerKeysRuntime(t)
	ctx := context.Background()
	for _, c := range []struct{ harness, action, provider, key string }{
		{"codex", "list", "", ""},
		{"pi", "add", "../x", testProviderKey},
		{"pi", "add", "zai", ""},
		{"pi", "add", "zai", "a\nb"},
		{"pi", "wipe", "zai", ""},
	} {
		_, err := r.HarnessProviderKeys(ctx, c.harness, c.action, c.provider, c.key)
		if err == nil || strings.Contains(err.Error(), testProviderKey) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}

func TestOpenCodeProviderKeys(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "opencode", "2.0.32")
	ctx := context.Background()
	got, err := r.HarnessProviderKeys(ctx, "opencode", "choices", "", "")
	if err != nil || len(got.Choices) != 1 || got.Choices[0].Name != "Z.AI" {
		t.Fatalf("choices: %+v %v", got, err)
	}
	got, err = r.HarnessProviderKeys(ctx, "opencode", "add", "zai", testProviderKey)
	if err != nil || len(got.Providers) != 1 || got.Providers[0].ID != "zai" {
		t.Fatalf("add: %+v %v", got, err)
	}
	home, _ := r.Accounts.HarnessHome("opencode")
	db := filepath.Join(home, "opencode.db")
	stdin, _ := os.ReadFile(db + ".stdin")
	argv, _ := os.ReadFile(db + ".argv")
	if !strings.Contains(string(stdin), `"key":"`+testProviderKey+`"`) || !strings.Contains(string(stdin), `"type":"key"`) {
		t.Fatalf("import stdin: %s", stdin)
	}
	if strings.Contains(string(argv), testProviderKey) {
		t.Fatalf("key in arguments: %s", argv)
	}
	if out, _ := json.Marshal(got); strings.Contains(string(out), testProviderKey) {
		t.Fatal("key in result")
	}
	got, err = r.HarnessProviderKeys(ctx, "opencode", "remove", "zai", "")
	if err != nil || len(got.Providers) != 0 {
		t.Fatalf("remove: %+v %v", got, err)
	}
}

func TestPiProviderKeysWaitForPiLock(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	home, _ := r.Accounts.HarnessHome("pi")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "auth.json")
	lock := path + ".lock"
	// Pi holds its lock: Dyna waits and then gives up without writing.
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err == nil {
		t.Fatal("wrote while Pi held the lock")
	}
	if fileExists(path) || !fileExists(lock) {
		t.Fatal("edited the file or broke a live lock")
	}
	// The saved account was edited first and must be restored, so the two homes agree.
	account, err := r.Accounts.Active("pi")
	if err == nil {
		t.Fatalf("activated an account after a failed edit: %+v", account)
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(home), "*", "auth.json"))
	for _, f := range entries {
		if raw, _ := os.ReadFile(f); strings.Contains(string(raw), testProviderKey) {
			t.Fatalf("account file kept the key after a failed edit: %s", f)
		}
	}
	// A lock older than Pi's stale limit is broken, and Dyna's own lock is released.
	old := time.Now().Add(-2 * piAuthStale)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := r.HarnessProviderKeys(context.Background(), "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	if fileExists(lock) || !fileExists(path) {
		t.Fatal("lock left behind or key not written")
	}
}

func TestPiProviderKeysRejectNullAuthFile(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	home, _ := r.Accounts.HarnessHome("pi")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "auth.json")
	if err := os.WriteFile(path, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.HarnessProviderKeys(context.Background(), "pi", "add", "zai", testProviderKey); err == nil {
		t.Fatal("added a key to a null login file")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "null" {
		t.Fatalf("file changed: %s", raw)
	}
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
	install(t, r.Bundles, "pi", "1.3.0")
	if _, err := r.Accounts.Active("pi"); err == nil {
		t.Fatal("fresh install already has an account")
	}
	if _, err := r.HarnessProviderKeys(context.Background(), "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	account, err := r.Accounts.Active("pi")
	if err != nil || !account.Active || account.Status == "not signed in" {
		t.Fatalf("account after adding a key: %+v %v", account, err)
	}
	raw, _ := os.ReadFile(filepath.Join(account.Home, "auth.json"))
	if !strings.Contains(string(raw), `"api_key"`) {
		t.Fatalf("account login file: %s", raw)
	}
	if _, err := r.HarnessProviderKeys(context.Background(), "pi", "remove", "zai", ""); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(filepath.Join(account.Home, "auth.json")); strings.Contains(string(raw), "zai") {
		t.Fatalf("key left in the account file: %s", raw)
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

func TestPiProviderKeyEditsKeepBothHomesInStep(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := r.HarnessProviderKeys(context.Background(), "pi", "add", "zai", fmt.Sprintf("test-key-%d", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	account, err := r.Accounts.Active("pi")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := r.Accounts.HarnessHome("pi")
	saved, _ := os.ReadFile(filepath.Join(account.Home, "auth.json"))
	runtime, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	if string(saved) != string(runtime) || !strings.Contains(string(saved), "test-key-") {
		t.Fatalf("homes differ: %s / %s", saved, runtime)
	}
}

// A saved snapshot can lose a key the runtime home still lists (a key moved
// to another provider). Removing it must clean the runtime home and report
// success instead of failing on the missing saved key.
func TestPiProviderKeyRemoveWhenTheSavedAccountLacksTheKey(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	account, err := r.Accounts.Active("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"deepseek":{"type":"api_key","key":"kept"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	home, _ := r.Accounts.HarnessHome("pi")
	got, err := r.HarnessProviderKeys(ctx, "pi", "remove", "zai", "")
	if err != nil {
		t.Fatalf("remove a key the saved account lacks: %v", err)
	}
	for _, p := range got.Providers {
		if p.ID == "zai" {
			t.Fatalf("still listed: %+v", got.Providers)
		}
	}
	runtimeHome, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	if strings.Contains(string(runtimeHome), "zai") {
		t.Fatalf("runtime auth.json still lists zai: %s", runtimeHome)
	}
	saved, _ := os.ReadFile(filepath.Join(account.Home, "auth.json"))
	if !strings.Contains(string(saved), "deepseek") {
		t.Fatalf("saved auth.json lost its key: %s", saved)
	}
}

func TestPiProviderKeyEditWaitsForAnAccountSwitch(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	dir, err := r.Accounts.harnessDirectory("pi")
	if err != nil {
		t.Fatal(err)
	}
	// A switch or native login holds this lock first.
	gate, err := acquireAccountLock(context.Background(), filepath.Join(dir, ".switch.lock"), syscall.LOCK_EX)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err = r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err == nil {
		t.Fatal("edited keys during an account switch")
	}
	if _, err = r.Accounts.Active("pi"); err == nil {
		t.Fatal("created an account during an account switch")
	}
	releaseAccountLock(gate)
	if _, err = r.HarnessProviderKeys(context.Background(), "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
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

func TestPiProviderKeyRollbackRemovesAnAccountFileThatDidNotExist(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	if _, err := r.HarnessProviderKeys(context.Background(), "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	account, _ := r.Accounts.Active("pi")
	file := filepath.Join(account.Home, "auth.json")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// Pi holds the runtime lock, so the runtime write fails after the account edit.
	home, _ := r.Accounts.HarnessHome("pi")
	if err := os.Mkdir(filepath.Join(home, "auth.json.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "openrouter", testProviderKey); err == nil {
		t.Fatal("added while Pi held the runtime lock")
	}
	if fileExists(file) {
		t.Fatal("the rollback left the new account file behind")
	}
}
