//go:build linux

package computers

import (
	"bufio"
	"context"
	"dyna-agent/internal/testsafe"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/settings"
)

func browserTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	t.Setenv("WSL_INTEROP", "")
	t.Setenv("WSL_DISTRO_NAME", "")
	t.Setenv("container", "")
}

func browserTestFile(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func browserTestRead(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBrowserProfileCopiesOnlyLoginState(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	files := []string{"Local State", "Profile 2/Preferences", "Profile 2/Cookies", "Profile 2/Network/Cookies", "Profile 2/Network/Cookies-wal", "Profile 2/Login Data", "Profile 2/Web Data"}
	for _, name := range files {
		browserTestFile(t, src, name, name)
	}
	for _, name := range []string{"SingletonLock", "Profile 2/History", "Profile 2/Cache/blob", "Profile 2/Local Storage/leveldb/00001.log"} {
		browserTestFile(t, src, name, "excluded")
	}
	if err := copyBrowserProfile(context.Background(), src, "Profile 2", dst, false); err != nil {
		t.Fatal(err)
	}
	var got []string
	_ = filepath.WalkDir(dst, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, _ := filepath.Rel(dst, path)
			got = append(got, rel)
		}
		return nil
	})
	want := []string{"Default/Cookies", "Default/Login Data", "Default/Network/Cookies", "Default/Preferences", "Default/Web Data", "Local State"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("copied %v, want %v", got, want)
	}
}

func TestBrowserProfileRejectsLinksAndBoundsStorage(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	browserTestFile(t, src, "Default/Local Storage/small", "small")
	browserTestFile(t, src, "Default/IndexedDB/large", "")
	f, err := os.OpenFile(filepath.Join(src, "Default/IndexedDB/large"), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(smallBrowserStorage + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := copyBrowserProfile(context.Background(), src, "Default", dst, true); err != nil {
		t.Fatal(err)
	}
	if browserTestRead(t, dst, "Default/Local Storage/small") != "small" {
		t.Fatal("lost storage")
	}
	if _, err := os.Stat(filepath.Join(dst, "Default/IndexedDB")); !os.IsNotExist(err) {
		t.Fatal("copied large IndexedDB")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "Default/Cookies")); err != nil {
		t.Fatal(err)
	}
	if err := copyBrowserProfile(context.Background(), src, "Default", t.TempDir(), true); err == nil {
		t.Fatal("followed a symlink")
	}
}

func TestBrowserMasterLockSerializesCopies(t *testing.T) {
	browserTestEnvironment(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withBrowserMaster(context.Background(), func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := withBrowserMaster(ctx, func() error { t.Error("entered locked master"); return nil }); err == nil {
		t.Fatal("lock did not wait")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := withBrowserMaster(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserModesAndSaveBack(t *testing.T) {
	browserTestEnvironment(t)
	ctx := context.Background()
	browserTestSQL(t, filepath.Join(browserMaster(), "Default/Cookies"), browserCookieSchema+"INSERT INTO cookies VALUES ('site', 'session', 'master', 1);")
	browserTestFile(t, browserMaster(), "Default/Preferences", "master preferences")
	browserTestFile(t, browserMaster(), "Default/Local Storage/state", "signed-in")
	v := settings.Defaults()
	if err := prepareBrowserProfile(ctx, "one", v); err != nil {
		t.Fatal(err)
	}
	if browserCookieValue(t, computerBrowserProfile("one")) != "master" {
		t.Fatal("did not seed from master")
	}
	browserTestSQL(t, filepath.Join(computerBrowserProfile("one"), "Default/Cookies"), "UPDATE cookies SET value='new', last_update_utc=2;")
	browserTestFile(t, computerBrowserProfile("one"), "Default/Preferences", "changed preferences")
	browserTestFile(t, computerBrowserProfile("one"), "Default/Cache/leak", "excluded")
	if err := prepareBrowserProfile(ctx, "one", v); err != nil {
		t.Fatal(err)
	}
	if browserCookieValue(t, computerBrowserProfile("one")) != "new" {
		t.Fatal("relaunch overwrote pending cookies")
	}
	if err := saveBrowserProfile(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if browserTestRead(t, browserMaster(), "Default/Preferences") != "master preferences" {
		t.Fatal("saved computer preferences")
	}
	if err := prepareBrowserProfile(ctx, "two", v); err != nil {
		t.Fatal(err)
	}
	if browserCookieValue(t, computerBrowserProfile("two")) != "new" {
		t.Fatal("save-back lost cookie")
	}
	if _, err := os.Stat(filepath.Join(browserMaster(), "Default/Cache")); !os.IsNotExist(err) {
		t.Fatal("saved cache")
	}
	v.BrowserSaveBack = false
	if err := prepareBrowserProfile(ctx, "no-save", v); err != nil {
		t.Fatal(err)
	}
	browserTestFile(t, computerBrowserProfile("no-save"), "Default/Cookies", "do not save")
	if err := saveBrowserProfile(ctx, "no-save"); err != nil {
		t.Fatal(err)
	}
	if browserCookieValue(t, browserMaster()) != "new" {
		t.Fatal("save-back ignored off")
	}
	v.BrowserProfileMode = "separate"
	if err := prepareBrowserProfile(ctx, "separate", v); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(computerBrowserProfile("separate"), "Default/Cookies")); !os.IsNotExist(err) {
		t.Fatal("separate copied master")
	}
	v.BrowserProfileMode = "bad"
	if err := prepareBrowserProfile(ctx, "bad", v); err == nil {
		t.Fatal("accepted bad mode")
	}
}

func TestBrowserCopyMineNeverWritesOwner(t *testing.T) {
	browserTestEnvironment(t)
	owner := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chromium")
	browserTestFile(t, owner, "Profile 2/Preferences", "{}")
	browserTestFile(t, owner, "Profile 2/Cookies", "owner cookie")
	browserTestFile(t, owner, "Local State", "{}")
	v := settings.Defaults()
	v.BrowserProfileMode = "copy-mine"
	v.BrowserOwnerProfile = "Profile 2"
	if err := prepareBrowserProfile(context.Background(), "copy", v); err != nil {
		t.Fatal(err)
	}
	if browserTestRead(t, computerBrowserProfile("copy"), "Default/Cookies") != "owner cookie" {
		t.Fatal("did not copy owner")
	}
	browserTestFile(t, computerBrowserProfile("copy"), "Default/Cookies", "changed")
	if err := saveBrowserProfile(context.Background(), "copy"); err != nil {
		t.Fatal(err)
	}
	if browserTestRead(t, owner, "Profile 2/Cookies") != "owner cookie" {
		t.Fatal("wrote owner profile")
	}
	t.Setenv("WSL_DISTRO_NAME", "test")
	if err := prepareBrowserProfile(context.Background(), "wsl", v); err == nil {
		t.Fatal("copy-mine available in WSL")
	}
}

func TestBrowserFlagsSwitchAndQuoting(t *testing.T) {
	browserTestEnvironment(t)
	browserTestFile(t, os.Getenv("XDG_CONFIG_HOME"), "chromium-flags.conf", "# comment\n--owner='two words'\n--literal=$(safe)\n")
	if err := prepareChromium("flags"); err != nil {
		t.Fatal(err)
	}
	v := settings.Defaults()
	v.BrowserExtraFlags = "--extra=ok"
	args, err := configuredBrowserFlags("flags", v)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "|")
	if !strings.Contains(joined, "--owner=two words|--literal=$(safe)") {
		t.Fatal(args)
	}
	v.BrowserUseFlagsFile = false
	args, err = configuredBrowserFlags("flags", v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, "|"), "--owner") {
		t.Fatal("owner flags leaked")
	}
	for _, text := range []string{"--user-data-dir=/owner", "--profile-directory=Default", "https://example.test", "--unclosed='"} {
		v.BrowserExtraFlags = text
		if _, err := configuredBrowserFlags("flags", v); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	got := stripBrowserProfileArgs([]string{"--headless", "--user-data-dir", "/owner", "--profile-directory=Profile 1", "--", "https://example.test"})
	if !reflect.DeepEqual(got, []string{"--headless", "https://example.test"}) {
		t.Fatal(got)
	}
}

func TestBrowserLiveSQLiteSnapshotIncludesWAL(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("live owner snapshots need sqlite3")
	}
	src, dst := filepath.Join(t.TempDir(), "Cookies"), filepath.Join(t.TempDir(), "Cookies")
	cmd := exec.Command(sqlite, src)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); _ = cmd.Wait() }()
	// Keep the source connection alive so committed data remains in its WAL.
	// The writer prints "ready" once the insert is committed; wait for that.
	_, _ = io.WriteString(input, "PRAGMA journal_mode=WAL; CREATE TABLE proof(value); INSERT INTO proof VALUES('saved'); SELECT 'ready';\n")
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if scanner.Text() == "ready" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("the writer exited before committing")
		}
	case <-time.After(9 * time.Second):
		t.Fatal("the writer did not commit")
	}
	if info, err := os.Stat(src + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the committed insert is not in the WAL: %v", err)
	}
	if err := snapshotBrowserDatabase(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst + "-wal"); !os.IsNotExist(err) {
		t.Fatal("copied live WAL separately")
	}
	out, err := exec.Command(sqlite, "-readonly", dst, "PRAGMA integrity_check; SELECT value FROM proof;").CombinedOutput()
	if err != nil || string(out) != "ok\nsaved\n" {
		t.Fatalf("snapshot: %s, %v", out, err)
	}
}

func TestBrowserLaunchReadsSettingsAndForcesBasicStore(t *testing.T) {
	browserTestEnvironment(t)
	if err := prepareChromium("launch"); err != nil {
		t.Fatal(err)
	}
	v := settings.Defaults()
	v.BrowserPath = "/custom/chromium"
	v.BrowserProfileMode = "separate"
	v.BrowserUseFlagsFile = false
	v.BrowserExtraFlags = "--password-store=gnome"
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WSL_INTEROP", "test")
	plan, err := prepareChromiumLaunch(context.Background(), "launch", []string{"--user-data-dir=/owner", "--profile-directory=Other", "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable != "/custom/chromium" {
		t.Fatal(plan.Executable)
	}
	if plan.Args[len(plan.Args)-1] != "--password-store=basic" {
		t.Fatal(plan.Args)
	}
	// --disable-notifications: an agent's browser shares the owner's session
	// bus, so a notification it showed would reach the owner's desktop.
	for _, flag := range []string{"--no-first-run", "--no-default-browser-check", "--disable-notifications"} {
		if !slices.Contains(plan.Args, flag) {
			t.Errorf("private browser launch lacks %s", flag)
		}
	}
	joined := strings.Join(plan.Args, "|")
	if strings.Contains(joined, "/owner") || strings.Contains(joined, "=Other") || !strings.Contains(joined, "--user-data-dir="+computerBrowserProfile("launch")) {
		t.Fatal(plan.Args)
	}
}

func TestLaunchSuppressesCrashRestoreBubble(t *testing.T) {
	browserTestEnvironment(t)
	id := "clean-exit"
	v := settings.Defaults()
	v.BrowserProfileMode = "separate"
	v.BrowserUseFlagsFile = false
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	// A killed last session leaves this crash state behind and Chromium opens
	// the "Restore pages?" bubble from it.
	browserTestFile(t, computerBrowserProfile(id), "Default/Preferences", `{"profile":{"exit_type":"Crashed","exited_cleanly":false},"session":{"restore_on_startup":4},"big":13312345678901234}`)
	plan, err := prepareChromiumLaunch(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Args, "|"), "--hide-crash-restore-bubble") {
		t.Fatal("launch args do not hide the crash-restore bubble:", plan.Args)
	}
	raw := browserTestRead(t, computerBrowserProfile(id), "Default/Preferences")
	var preferences map[string]any
	if err := json.Unmarshal([]byte(raw), &preferences); err != nil {
		t.Fatal(err)
	}
	profile, _ := preferences["profile"].(map[string]any)
	if profile["exit_type"] != "Normal" || profile["exited_cleanly"] != true {
		t.Fatalf("crash state survived: %v", profile)
	}
	session, _ := preferences["session"].(map[string]any)
	if session["restore_on_startup"] != 4.0 {
		t.Fatal("lost other preferences:", raw)
	}
	if !strings.Contains(raw, "13312345678901234") {
		t.Fatal("large preference numbers changed:", raw)
	}
}

func TestMarkBrowserCleanExitKeepsPreferencesOnFailure(t *testing.T) {
	browserTestEnvironment(t)
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict root")
	}
	id := "clean-exit-failure"
	const original = `{"profile":{"exit_type":"Crashed"}}`
	browserTestFile(t, computerBrowserProfile(id), "Default/Preferences", original)
	// A read-only profile directory makes the atomic replacement fail after
	// the read. The failed write must leave the old Preferences intact.
	directory := filepath.Join(computerBrowserProfile(id), "Default")
	if err := os.Chmod(directory, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
	if err := markBrowserCleanExit(id); err == nil {
		t.Fatal("a write into a read-only profile directory succeeded")
	}
	if got := browserTestRead(t, computerBrowserProfile(id), "Default/Preferences"); got != original {
		t.Fatal("failed replacement damaged Preferences:", got)
	}
}

const browserCookieSchema = `CREATE TABLE cookies(host_key TEXT, name TEXT, value TEXT, last_update_utc INTEGER, UNIQUE(host_key, name));`

func browserTestSQL(t *testing.T, path, sql string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("SQLite tests need sqlite3")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sqlite3", "-bail", path, sql).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func browserCookieValue(t *testing.T, root string) string {
	t.Helper()
	return browserTestSQL(t, filepath.Join(root, "Default/Cookies"), "SELECT value FROM cookies WHERE host_key='site';")
}

func TestBrowserSharedMergeConcurrentComputers(t *testing.T) {
	for _, cookiePath := range []string{"Cookies", "Network/Cookies"} {
		t.Run(cookiePath, func(t *testing.T) {
			browserTestEnvironment(t)
			ctx := context.Background()
			master := filepath.Join(browserMaster(), "Default", cookiePath)
			browserTestSQL(t, master, browserCookieSchema+"INSERT INTO cookies VALUES('seed','session','seed',1);")
			loginSchema := `CREATE TABLE logins(id INTEGER PRIMARY KEY, origin_url TEXT, username_value TEXT, password_value BLOB, date_password_modified INTEGER, UNIQUE(origin_url, username_value));`
			browserTestSQL(t, filepath.Join(browserMaster(), "Default/Login Data"), loginSchema)
			browserTestFile(t, browserMaster(), "Default/Web Data", "initial web")
			browserTestFile(t, browserMaster(), "Default/Local Storage/state", "initial storage")
			browserTestFile(t, browserMaster(), "Default/IndexedDB/state", "initial indexed")
			for _, id := range []string{"a", "b", "unchanged"} {
				if err := prepareBrowserProfile(ctx, id, settings.Defaults()); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"a", "b"} {
				root := computerBrowserProfile(id)
				browserTestSQL(t, filepath.Join(root, "Default", cookiePath), "INSERT INTO cookies VALUES('"+id+"','session','saved',2);")
				// Each database allocates ID 1 for a different login.
				browserTestSQL(t, filepath.Join(root, "Default/Login Data"), "INSERT INTO logins VALUES(1,'"+id+"','user',X'1234',2);")
			}
			browserTestFile(t, computerBrowserProfile("a"), "Default/Web Data", "new web")
			browserTestFile(t, computerBrowserProfile("a"), "Default/Local Storage/state", "new storage")
			browserTestFile(t, computerBrowserProfile("a"), "Default/IndexedDB/state", "new indexed")
			for _, id := range []string{"a", "b"} {
				if err := saveBrowserProfile(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if got := browserTestSQL(t, master, "SELECT group_concat(host_key) FROM (SELECT host_key FROM cookies ORDER BY host_key);"); got != "a,b,seed" {
				t.Fatal(got)
			}
			if got := browserTestSQL(t, filepath.Join(browserMaster(), "Default/Login Data"), "SELECT count(*) FROM logins;"); got != "2" {
				t.Fatal(got)
			}
			for name, want := range map[string]string{"Web Data": "new web", "Local Storage/state": "new storage", "IndexedDB/state": "new indexed"} {
				if got := browserTestRead(t, browserMaster(), "Default/"+name); got != want {
					t.Fatalf("%s: %s", name, got)
				}
			}
			before, err := browserStateHash(browserMaster())
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(master)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveBrowserProfile(ctx, "unchanged"); err != nil {
				t.Fatal(err)
			}
			after, err := browserStateHash(browserMaster())
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(master)
			if err != nil {
				t.Fatal(err)
			}
			if before != after || !os.SameFile(info, afterInfo) {
				t.Fatal("unchanged computer rewrote master")
			}
		})
	}
}

func TestBrowserMergeNewestRowsWin(t *testing.T) {
	for _, tc := range []struct{ name, table, schema, timestamp string }{
		{"Cookies", "cookies", browserCookieSchema, "last_update_utc"},
		{"Login Data", "logins", `CREATE TABLE logins(host_key TEXT, name TEXT, value TEXT, date_password_modified INTEGER, extra BLOB, UNIQUE(host_key, name));`, "date_password_modified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dst := filepath.Join(t.TempDir(), tc.name), filepath.Join(t.TempDir(), tc.name)
			extra := ""
			if tc.table == "logins" {
				extra = ",X'1234'"
			}
			browserTestSQL(t, dst, tc.schema+"INSERT INTO "+tc.table+" VALUES('site','session','master',10"+extra+");")
			browserTestSQL(t, src, tc.schema+"INSERT INTO "+tc.table+" VALUES('site','session','old',9"+extra+");")
			if err := mergeBrowserDatabase(context.Background(), src, dst, tc.table, tc.timestamp); err != nil {
				t.Fatal(err)
			}
			if got := browserTestSQL(t, dst, "SELECT value FROM "+tc.table+";"); got != "master" {
				t.Fatal(got)
			}
			browserTestSQL(t, src, "UPDATE "+tc.table+" SET value='new', "+tc.timestamp+"=11;")
			if err := mergeBrowserDatabase(context.Background(), src, dst, tc.table, tc.timestamp); err != nil {
				t.Fatal(err)
			}
			if got := browserTestSQL(t, dst, "SELECT value FROM "+tc.table+";"); got != "new" {
				t.Fatal(got)
			}
			browserTestSQL(t, src, "DELETE FROM "+tc.table+";")
			if err := mergeBrowserDatabase(context.Background(), src, dst, tc.table, tc.timestamp); err != nil {
				t.Fatal(err)
			}
			if got := browserTestSQL(t, dst, "SELECT count(*) FROM "+tc.table+";"); got != "1" {
				t.Fatal("logout deleted master row")
			}
		})
	}
}

func TestBrowserSharedNewDatabaseAndStorageConflict(t *testing.T) {
	browserTestEnvironment(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := prepareBrowserProfile(ctx, id, settings.Defaults()); err != nil {
			t.Fatal(err)
		}
		browserTestFile(t, computerBrowserProfile(id), "Default/Local Storage/state", id)
		browserTestFile(t, computerBrowserProfile(id), "Default/IndexedDB/state", id)
		browserTestSQL(t, filepath.Join(computerBrowserProfile(id), "Default/Web Data"), "CREATE TABLE web(value); INSERT INTO web VALUES('"+id+"');")
		browserTestSQL(t, filepath.Join(computerBrowserProfile(id), "Default/Cookies"), browserCookieSchema+"INSERT INTO cookies VALUES('"+id+"','session','saved',1);")
	}
	for _, id := range []string{"a", "b"} {
		if err := saveBrowserProfile(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Local Storage/state", "IndexedDB/state"} {
		if got := browserTestRead(t, browserMaster(), "Default/"+name); got != "b" {
			t.Fatal(got)
		}
	}
	if got := browserTestSQL(t, filepath.Join(browserMaster(), "Default/Web Data"), "SELECT value FROM web;"); got != "b" {
		t.Fatal(got)
	}
	if got := browserTestSQL(t, filepath.Join(browserMaster(), "Default/Cookies"), "SELECT count(*) FROM cookies;"); got != "2" {
		t.Fatal(got)
	}
}

// A merge runs sqlite3 as often as the schema needs. On a loaded machine one
// run can take over five seconds, so only the caller's context may stop it.
func TestBrowserSQLiteStopsWhenTheCallerCancels(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	if err := syscall.Mkfifo(started, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "sqlite3")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho started > \""+started+"\"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := browserSQLite(ctx, filepath.Join(dir, "db"), "PRAGMA index_list('cookies');")
		done <- err
	}()
	// Open read-write so the open never blocks; Close unblocks the read.
	fifo, err := os.OpenFile(started, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	ready := make(chan struct{})
	go func() {
		if _, err := fifo.Read(make([]byte, 1)); err == nil {
			close(ready)
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("sqlite3 exited before it started: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("the fake sqlite3 did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled caller did not stop sqlite3")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sqlite3 kept running after the caller cancelled")
	}
}
