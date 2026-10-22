package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

func museTestActivate(m *museManager, ctx context.Context, integration string, c store.Credential) error {
	_, err := m.Activate(ctx, integration, c)
	return err
}

func museTestManager(t *testing.T, values map[string]string) *museManager {
	t.Helper()
	return &museManager{host: Host{Home: t.TempDir(), Executable: "/fake/muse", Env: func(k string) string { return values[k] }}}
}

func museTestWrite(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

func museTestCredential(t *testing.T, raw string) store.Credential {
	t.Helper()
	payload, err := json.Marshal(map[string]json.RawMessage{"metaCredential": json.RawMessage(raw)})
	if err != nil {
		t.Fatal("fixture must be valid JSON")
	}
	return store.Credential{Schema: museAuthSchema, Payload: payload}
}

func TestMuseCaptureAndActivationPreserveOtherRecords(t *testing.T) {
	m := museTestManager(t, nil)
	path, _ := m.authPath("")
	museTestWrite(t, path, `{"schema_version":1,"future":{"keep":true},"providers":{"other":{"api_key":"other-fixture"},"meta":{"mechanism":"oauth","access_token":"old-fixture","api_key":"old-key-fixture","user_email":"Outgoing@example.test"}},"mcp_oauth":{"keep":"fixture"}}`)
	ctx := context.Background()
	before, err := m.Capture(ctx, "")
	if err != nil || len(before) != 1 || before[0].Identity != "meta:email:outgoing@example.test" {
		t.Fatal("outgoing identity capture failed")
	}
	lockPath := filepath.Join(filepath.Dir(path), ".auth.json.lock")
	museTestWrite(t, lockPath, "")
	lockBefore, _ := os.Stat(lockPath)
	target := museTestCredential(t, `{"mechanism":"oauth","access_token":"target-fixture","api_key":"target-key-fixture","user_email":"Target@Example.test","expires_at":2000000000,"future":[1,2]}`)
	if err := museTestActivate(m, ctx, "", target); err != nil {
		t.Fatal(err)
	}
	after, err := m.Capture(ctx, "")
	if err != nil || len(after) != 1 || after[0].Identity != "meta:email:target@example.test" || after[0].Label != "Target@Example.test" || !after[0].ExpiresAt.Equal(time.Unix(2000000000, 0)) {
		t.Fatal("target identity capture failed")
	}
	var want, got any
	_ = json.Unmarshal(target.Payload, &want)
	_ = json.Unmarshal(after[0].Credential.Payload, &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatal("complete target credential was not preserved")
	}
	doc, err := museReadAuth(path)
	var future map[string]bool
	var mcp map[string]string
	_ = json.Unmarshal(doc["future"], &future)
	_ = json.Unmarshal(doc["mcp_oauth"], &mcp)
	if err != nil || !future["keep"] || mcp["keep"] != "fixture" {
		t.Fatal("unrelated root records changed")
	}
	providers, _ := museProviders(doc)
	var other map[string]string
	_ = json.Unmarshal(providers["other"], &other)
	if other["api_key"] != "other-fixture" {
		t.Fatal("another provider changed")
	}
	info, _ := os.Stat(path)
	lockAfter, _ := os.Stat(lockPath)
	if info.Mode().Perm() != 0600 || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("permissions or companion lock inode changed")
	}
	if err := museTestActivate(m, ctx, "", before[0].Credential); err != nil {
		t.Fatal("capture-back credential could not be restored")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".auth.json-") {
			t.Fatal("temporary credential file was left behind")
		}
	}
}

func TestMuseCaptureRefusesUnsupportedOrUnidentifiedCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      error
	}{
		{"keychain", `{"storage":"keychain","mechanism":"oauth","user_email":"private@example.test"}`, ErrUnsupported},
		{"implicit-keychain", `{"mechanism":"oauth","user_email":"private@example.test"}`, ErrUnsupported},
		{"no-email", `{"api_key":"fixture-private-key"}`, ErrNoIdentity},
		{"empty", `{}`, ErrNotSignedIn},
		{"bad-email", `{"api_key":"fixture-private-key","user_email":"bad\nemail"}`, ErrNoIdentity},
		{"malformed", `{"api_key":123,"user_email":"private@example.test"}`, ErrSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := museTestManager(t, nil)
			path, _ := m.authPath("")
			raw := `{"schema_version":1,"providers":{"meta":` + tc.raw + `}}`
			museTestWrite(t, path, raw)
			_, err := m.Capture(context.Background(), "")
			if !errors.Is(err, tc.want) {
				t.Fatal("incorrect refusal")
			}
			if strings.Contains(err.Error(), "fixture-private-key") || strings.Contains(err.Error(), "private@example.test") {
				t.Fatal("error disclosed private material")
			}
			target := museTestCredential(t, `{"api_key":"target-fixture","user_email":"target@example.test"}`)
			if tc.want != ErrNotSignedIn && museTestActivate(m, context.Background(), "", target) == nil {
				t.Fatal("unsafe outgoing state was overwritten")
			}
		})
	}
}

func TestMusePathsAndIsolatedLogin(t *testing.T) {
	xdg := t.TempDir()
	m := museTestManager(t, map[string]string{"XDG_CONFIG_HOME": xdg, "MUSE_AUTH_PATH": "/unused/launcher-only"})
	path, err := m.authPath("")
	if err != nil || path != filepath.Join(xdg, "muse", "auth.json") {
		t.Fatal("native XDG path was not used")
	}
	root := t.TempDir()
	argv, env, err := m.LoginCommand(root, "")
	if err != nil || !reflect.DeepEqual(argv, []string{"/fake/muse", "login"}) {
		t.Fatal("isolated login command is incorrect")
	}
	got := map[string]string{}
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		got[key] = value
	}
	if got["HOME"] != root || got["XDG_CONFIG_HOME"] != filepath.Join(root, ".config") || got["XDG_DATA_HOME"] != filepath.Join(root, ".local", "share") || got["TBH_CREDENTIAL_BACKEND"] != "file" {
		t.Fatal("login roots or backend are not isolated")
	}
	if v, ok := got["META_API_KEY"]; !ok || v != "" {
		t.Fatal("login inherits an overriding API key")
	}
	isolatedPath, _ := m.authPath(root)
	museTestWrite(t, isolatedPath, `{"schema_version":1,"providers":{"meta":{"api_key":"isolated-fixture","user_email":"isolated@example.test"}}}`)
	accounts, err := m.Capture(context.Background(), root)
	if err != nil || len(accounts) != 1 || accounts[0].Identity != "meta:email:isolated@example.test" {
		t.Fatal("capture did not use the isolated login root")
	}
	for _, key := range []string{"META_API_KEY", "TBH_CREDENTIAL_BACKEND"} {
		m.host.Env = func(k string) string {
			if k == key {
				return "fixture-override"
			}
			return ""
		}
		if _, err := m.Capture(context.Background(), ""); !errors.Is(err, ErrUnsupported) {
			t.Fatal("effective environment override was ignored")
		}
		if _, err := m.Capture(context.Background(), root); err != nil {
			t.Fatal("host environment leaked into isolated capture")
		}
	}
	if argv, _, err := m.LoginCommand("", ""); err != nil || !reflect.DeepEqual(argv, []string{"/fake/muse", "login"}) {
		t.Fatal("in-place login command failed")
	}
}

func TestMuseActivationLockAndValidation(t *testing.T) {
	m := museTestManager(t, nil)
	path, _ := m.authPath("")
	museTestWrite(t, path, `{"schema_version":1,"providers":{"meta":{"api_key":"old-fixture","user_email":"old@example.test"}}}`)
	old, _ := os.ReadFile(path)
	lockPath := filepath.Join(filepath.Dir(path), ".auth.json.lock")
	lock, err := museLock(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	target := museTestCredential(t, `{"api_key":"target-fixture","user_email":"target@example.test"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := museTestActivate(m, ctx, "", target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("activation did not respect the companion lock and deadline")
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
	invalid := []store.Credential{
		{Schema: "wrong", Payload: target.Payload},
		{Schema: museAuthSchema, Payload: json.RawMessage(`{"metaCredential":null}`)},
		{Schema: museAuthSchema, Payload: json.RawMessage(`{"metaCredential":{"api_key":"private-fixture"}}`)},
	}
	for _, c := range invalid {
		if museTestActivate(m, context.Background(), "", c) == nil {
			t.Fatal("invalid target was accepted")
		}
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(old) {
		t.Fatal("failed activation changed native credentials")
	}
	if _, err := m.Refresh(context.Background(), Captured{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("refresh should remain unsupported")
	}
}

func TestMuseActivationCreatesFileAndRejectsSymlink(t *testing.T) {
	m := museTestManager(t, nil)
	ctx := context.Background()
	if _, err := m.Capture(ctx, ""); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("missing auth file should be signed out")
	}
	target := museTestCredential(t, `{"api_key":"target-fixture","user_email":"target@example.test"}`)
	if err := museTestActivate(m, ctx, "", target); err != nil {
		t.Fatal(err)
	}
	path, _ := m.authPath("")
	backup := filepath.Join(t.TempDir(), "original.json")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, path); err != nil {
		t.Fatal(err)
	}
	if museTestActivate(m, ctx, "", target) == nil {
		t.Fatal("symlinked auth file was overwritten")
	}
	if _, err := m.Capture(ctx, ""); err == nil {
		t.Fatal("symlinked auth file was read")
	}
}
