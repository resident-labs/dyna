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

// A key belongs to the account of its provider. Pi reads one provider per
// account, so adding one key never touches another provider's login.
func TestPiProviderKeys(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	got, err := r.HarnessProviderKeys(ctx, "pi", "choices", "", "")
	if err != nil || len(got.Choices) != 2 || got.Choices[0].ID != "openrouter" {
		t.Fatalf("choices: %+v %v", got, err)
	}
	xai := seedPiAccount(t, r, "xai", `{"type":"oauth","access":"keep"}`)
	got, err = r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey)
	if err != nil || len(got.Providers) != 2 || got.Providers[0].ID != "xai" || got.Providers[0].Auth != "oauth" || got.Providers[1].Auth != "api_key" {
		t.Fatalf("add: %+v %v", got, err)
	}
	zai, found, err := r.piAccountForProvider(ctx, store.ProviderZAI)
	if err != nil || !found || zai.ID == xai.ID {
		t.Fatalf("zai account: %+v %v", zai, err)
	}
	path := filepath.Join(zai.Home, "auth.json")
	raw, _ := os.ReadFile(path)
	var file map[string]map[string]string
	if json.Unmarshal(raw, &file) != nil || file["zai"]["type"] != "api_key" || file["zai"]["key"] != testProviderKey {
		t.Fatalf("zai auth.json: %s", raw)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if raw, _ := os.ReadFile(filepath.Join(xai.Home, "auth.json")); !strings.Contains(string(raw), "keep") {
		t.Fatalf("the xai login changed: %s", raw)
	}
	if out, _ := json.Marshal(got); strings.Contains(string(out), testProviderKey) {
		t.Fatal("key in result")
	}
	got, err = r.HarnessProviderKeys(ctx, "pi", "remove", "zai", "")
	if err != nil || len(got.Providers) != 1 || got.Providers[0].ID != "xai" {
		t.Fatalf("remove: %+v %v", got, err)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "zai") {
		t.Fatalf("zai auth.json after remove: %s", raw)
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

// An OpenCode Go API key creates an OpenCode account, which is what an
// OpenCode profile runs. The key goes into that account's own home, not the
// shared OpenCode home.
func TestOpenCodeKeyCreatesAnOpenCodeAccount(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "opencode", "2.0.32")
	ctx := context.Background()
	account, err := r.AddProviderKey(ctx, "opencode", store.ProviderOpenCodeGo, "Go work", testProviderKey)
	if err != nil {
		t.Fatal(err)
	}
	if account.Harness != "opencode" || account.Provider != store.ProviderOpenCodeGo || account.Status != "ready" || !account.Active || account.Label != "Go work" {
		t.Fatalf("account = %+v", account)
	}
	// The key went into OpenCode's own store, and the account owns the captured
	// identity of that key.
	shared, _ := r.Accounts.HarnessHome("opencode")
	stdin, _ := os.ReadFile(filepath.Join(shared, "opencode.db") + ".stdin")
	if !strings.Contains(string(stdin), `"integrationID":"opencode-go"`) || !strings.Contains(string(stdin), `"key":"`+testProviderKey+`"`) {
		t.Fatalf("the key was not imported: %s", stdin)
	}
	if !strings.HasPrefix(account.Identity, "opencode:opencode-go:") {
		t.Fatalf("the account identity = %q", account.Identity)
	}
}

func TestPiProviderKeysWaitForPiLock(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	account := seedPiAccount(t, r, "zai", `{"type":"api_key","key":"old"}`)
	path := filepath.Join(account.Home, "auth.json")
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
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), testProviderKey) || !fileExists(lock) {
		t.Fatal("edited the file or broke a live lock")
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
	account := seedPiAccount(t, r, "zai", `{"type":"api_key","key":"old"}`)
	path := filepath.Join(account.Home, "auth.json")
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
	ctx := context.Background()
	if _, found, err := r.piAccountForProvider(ctx, store.ProviderZAI); err != nil || found {
		t.Fatalf("fresh install already has a zai account: %+v %v", found, err)
	}
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err != nil {
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

func TestPiProviderKeyEditsOfOneProviderKeepOneAccount(t *testing.T) {
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
	account, found, err := r.piAccountForProvider(context.Background(), store.ProviderZAI)
	if err != nil || !found {
		t.Fatalf("zai account: %+v %v", account, err)
	}
	saved, _ := os.ReadFile(filepath.Join(account.Home, "auth.json"))
	if !strings.Contains(string(saved), "test-key-") {
		t.Fatalf("account login: %s", saved)
	}
	accounts, _ := r.Accounts.List()
	if len(accounts) != 1 {
		t.Fatalf("concurrent adds made %d accounts", len(accounts))
	}
}

// A login file can lose an entry the account still lists. Removing that key
// must report success and keep the other provider's entry.
func TestPiProviderKeyRemoveWhenTheAccountLacksTheKey(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	account, found, err := r.piAccountForProvider(ctx, store.ProviderZAI)
	if err != nil || !found {
		t.Fatalf("zai account: %+v %v", account, err)
	}
	if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"deepseek":{"type":"api_key","key":"kept"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := r.HarnessProviderKeys(ctx, "pi", "remove", "zai", "")
	if err != nil {
		t.Fatalf("remove a key the account lacks: %v", err)
	}
	for _, p := range got.Providers {
		if p.ID == "zai" {
			t.Fatalf("still listed: %+v", got.Providers)
		}
	}
	saved, _ := os.ReadFile(filepath.Join(account.Home, "auth.json"))
	if !strings.Contains(string(saved), "deepseek") {
		t.Fatalf("account login lost its key: %s", saved)
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
	if _, found, err := r.piAccountForProvider(context.Background(), store.ProviderZAI); err != nil || found {
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
