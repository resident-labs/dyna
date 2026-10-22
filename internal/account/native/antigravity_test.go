package native

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type agyFakeStore struct {
	raw           []byte
	readErr       error
	writeErr      error
	reads, writes int
	failReadAt    int
	omitWrite     bool
}

func (s *agyFakeStore) Read(context.Context) ([]byte, error) {
	s.reads++
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.failReadAt == s.reads {
		return nil, errors.New("fake read failure")
	}
	if len(s.raw) == 0 {
		return nil, ErrNotSignedIn
	}
	return append([]byte(nil), s.raw...), nil
}
func (s *agyFakeStore) Write(_ context.Context, raw []byte) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	if !s.omitWrite {
		s.raw = append([]byte(nil), raw...)
	}
	return nil
}
func agyTestWrapper(t *testing.T, sub string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"token": map[string]string{"access_token": "fake-access-" + sub, "refresh_token": "fake-refresh-" + sub, "expiry": "2100-01-01T00:00:00Z"}, "id_token": testJWT(sub, 4102444800), "auth_method": "consumer", "project_id": "fake-project", "region": "fake-region", "user_tier": "fake-tier", "future_field": map[string]bool{"keep": true}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func agyTestManager(t *testing.T, store agyTokenStore) *antigravityManager {
	t.Helper()
	return &antigravityManager{host: testHost(t, nil), storeOverride: store}
}
func TestAntigravityCaptureAndFullWrapperActivation(t *testing.T) {
	ctx := context.Background()
	s := &agyFakeStore{raw: agyTestWrapper(t, "source")}
	m := agyTestManager(t, s)
	before, err := m.Capture(ctx, "")
	if err != nil || len(before) != 1 || before[0].Identity != "google:source" {
		t.Fatal("capture failed", err)
	}
	target, err := agyAccount(agyTestWrapper(t, "target"))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := m.Activate(ctx, "", target.Credential)
	if err != nil || len(previous) != 1 || previous[0].Identity != "google:source" {
		t.Fatal("activation lost outgoing account", err)
	}
	after, err := m.Capture(ctx, "")
	if err != nil || after[0].Identity != "google:target" {
		t.Fatal("target not active", err)
	}
	var got map[string]json.RawMessage
	_ = json.Unmarshal(s.raw, &got)
	if got["future_field"] == nil || got["project_id"] == nil || got["region"] == nil {
		t.Fatal("wrapper fields lost")
	}
	if s.writes != 1 {
		t.Fatal("capture or verification mutated credentials")
	}
}
func TestAntigravityRefusesIdentitylessOrUnsupportedWrapper(t *testing.T) {
	raw := agyTestWrapper(t, "source")
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	delete(doc, "id_token")
	missing, _ := json.Marshal(doc)
	if _, err := agyAccount(missing); !errors.Is(err, ErrNoIdentity) {
		t.Fatal("accepted wrapper without ID token")
	}
	doc["id_token"] = json.RawMessage(`"malformed"`)
	bad, _ := json.Marshal(doc)
	if _, err := agyAccount(bad); !errors.Is(err, ErrNoIdentity) {
		t.Fatal("accepted malformed ID token")
	}
	_ = json.Unmarshal(raw, &doc)
	doc["auth_method"] = json.RawMessage(`"unsupported"`)
	bad, _ = json.Marshal(doc)
	if _, err := agyAccount(bad); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted unknown auth method")
	}
	doc["auth_method"] = json.RawMessage(`"gcp"`)
	delete(doc, "project_id")
	bad, _ = json.Marshal(doc)
	if _, err := agyAccount(bad); !errors.Is(err, ErrSchema) {
		t.Fatal("accepted incomplete GCP onboarding")
	}
}
func TestAntigravityVerificationFailureRollsBackAndReturnsPrevious(t *testing.T) {
	source := agyTestWrapper(t, "source")
	s := &agyFakeStore{raw: source, failReadAt: 2}
	m := agyTestManager(t, s)
	target, _ := agyAccount(agyTestWrapper(t, "target"))
	previous, err := m.Activate(context.Background(), "", target.Credential)
	if !errors.Is(err, ErrPartialActivation) || len(previous) != 1 || previous[0].Identity != "google:source" {
		t.Fatal("verification failure lost capture")
	}
	if string(s.raw) != string(source) || s.writes != 2 {
		t.Fatal("verification failure did not restore complete wrapper")
	}
}
func TestAntigravityWriteFailureStillReturnsPrevious(t *testing.T) {
	s := &agyFakeStore{raw: agyTestWrapper(t, "source"), writeErr: errors.New("fake write failure")}
	m := agyTestManager(t, s)
	target, _ := agyAccount(agyTestWrapper(t, "target"))
	previous, err := m.Activate(context.Background(), "", target.Credential)
	if !errors.Is(err, ErrPartialActivation) || len(previous) != 1 || previous[0].Identity != "google:source" {
		t.Fatal("write failure lost outgoing capture")
	}
}
func TestAntigravityRejectsUnreadableOutgoing(t *testing.T) {
	s := &agyFakeStore{raw: agyTestWrapper(t, "source"), readErr: errors.New("fake unavailable keyring")}
	m := agyTestManager(t, s)
	target, _ := agyAccount(agyTestWrapper(t, "target"))
	if _, err := m.Activate(context.Background(), "", target.Credential); err == nil || s.writes != 0 {
		t.Fatal("overwrote unreadable native login")
	}
}

func TestAntigravityLoginAndRefreshCapabilities(t *testing.T) {
	m := agyTestManager(t, &agyFakeStore{})
	m.host.Executable = "/test/agy"
	argv, env, err := m.LoginCommand("", "")
	if err != nil || len(argv) != 1 || argv[0] != "/test/agy" || len(env) == 0 {
		t.Fatal("in-place command failed")
	}
	root := t.TempDir()
	_, isolated, err := m.LoginCommand(root, "")
	if err != nil || !strings.Contains(strings.Join(isolated, "\n"), "HOME="+root) {
		t.Fatal("isolated login failed", err)
	}
	if _, err := m.Refresh(context.Background(), Captured{}); !errors.Is(err, ErrSchema) {
		t.Fatal("refresh accepted an invalid schema")
	}
	if !m.Caps().IsolatedLogin || !m.Caps().Refresh {
		t.Fatal("incorrect capabilities")
	}
	if _, err := m.Capture(context.Background(), t.TempDir()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("isolated capture failed")
	}
}
func TestAntigravitySecretToolOnlyReadsExactAttributes(t *testing.T) {
	h := testHost(t, nil)
	helper := filepath.Join(h.Home, "fake-secret-tool")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/arguments\"\n/bin/cat \"$HOME/read\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	raw := agyTestWrapper(t, "fixture")
	if err := os.WriteFile(filepath.Join(h.Home, "read"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := (agySecretTool{path: helper, host: h}).Read(context.Background())
	if err != nil || string(got) != string(raw) {
		t.Fatal("lookup failed", err)
	}
	args, _ := os.ReadFile(filepath.Join(h.Home, "arguments"))
	if string(args) != "lookup\nservice\ngemini\nusername\nantigravity\n" {
		t.Fatal("wrong lookup attributes")
	}
}
func TestAntigravityMissingHelperAndRedactedErrors(t *testing.T) {
	h := testHost(t, nil)
	bridge := agySecretTool{path: filepath.Join(h.Home, "absent"), host: h}
	if _, err := bridge.Read(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("missing helper was not unsupported")
	}
	helper := filepath.Join(h.Home, "fake-failure")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'fake-secret-error' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	bridge.path = helper
	if _, err := bridge.Read(context.Background()); err == nil || errors.Is(err, ErrNotSignedIn) || strings.Contains(err.Error(), "fake-secret-error") {
		t.Fatal("helper error was leaked or mistaken for no login")
	}
}
func TestAntigravityRegistration(t *testing.T) {
	m, ok := For("antigravity", testHost(t, nil))
	if !ok || m.Harness() != "antigravity" || !m.Caps().Activate {
		t.Fatal("registration failed")
	}
}

func TestAntigravityPrivateFileImportsOnceAndSwitchesWithoutKeyringWrites(t *testing.T) {
	ctx := context.Background()
	legacy := &agyFakeStore{raw: agyTestWrapper(t, "legacy")}
	m := &antigravityManager{host: testHost(t, AntigravityFileEnv()), legacy: legacy}
	first, err := m.Capture(ctx, "")
	if err != nil || len(first) != 1 || first[0].Identity != "google:legacy" {
		t.Fatal("legacy import failed", err)
	}
	path := filepath.Join(m.host.Home, AntigravityAuthFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private file mode", err)
	}
	legacy.readErr = errors.New("keyring must not be used again")
	target, _ := agyAccount(agyTestWrapper(t, "private"))
	old, err := m.Activate(ctx, "", target.Credential)
	if err != nil || len(old) != 1 || old[0].Identity != "google:legacy" {
		t.Fatal("file activation failed", err)
	}
	after, err := m.Capture(ctx, "")
	if err != nil || after[0].Identity != "google:private" {
		t.Fatal("private login lost", err)
	}
	if legacy.reads != 1 || legacy.writes != 0 {
		t.Fatal("touched shared credentials after import")
	}
	raw, _ := os.ReadFile(path)
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(target.Credential.Payload, &payload)
	if string(raw) != string(payload[agyRole]) {
		t.Fatal("wrapper changed")
	}
}
func TestAntigravityIsolatedFileDoesNotImportOwner(t *testing.T) {
	legacy := &agyFakeStore{raw: agyTestWrapper(t, "owner")}
	m := &antigravityManager{host: testHost(t, nil), legacy: legacy}
	root := t.TempDir()
	if _, err := m.Capture(context.Background(), root); !errors.Is(err, ErrNotSignedIn) || legacy.reads != 0 {
		t.Fatal("isolated login read owner credentials", err)
	}
	path := filepath.Join(root, AntigravityAuthFile)
	testWrite(t, path, json.RawMessage(agyTestWrapper(t, "isolated")))
	got, err := m.Capture(context.Background(), root)
	if err != nil || got[0].Identity != "google:isolated" {
		t.Fatal("isolated file capture failed", err)
	}
}
func TestAntigravityFileRefusesInvalidAndLinkedCredentials(t *testing.T) {
	root := t.TempDir()
	m := &antigravityManager{host: testHost(t, nil)}
	path := filepath.Join(root, AntigravityAuthFile)
	testWrite(t, path, map[string]string{"bad": "wrapper"})
	if _, err := m.Capture(context.Background(), root); err == nil {
		t.Fatal("accepted malformed private file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(t.TempDir(), "owner-token")
	testWrite(t, owner, json.RawMessage(agyTestWrapper(t, "owner")))
	if err := os.Symlink(owner, path); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Capture(context.Background(), root); err == nil {
		t.Fatal("followed credential symlink")
	}
	if err := (agyFileStore{path: path}).Write(context.Background(), agyTestWrapper(t, "target")); err == nil {
		t.Fatal("overwrote credential symlink")
	}
}

func TestAntigravityOwnerCaptureDoesNotWriteOwnerFiles(t *testing.T) {
	legacy := &agyFakeStore{raw: agyTestWrapper(t, "owner")}
	m := &antigravityManager{host: testHost(t, nil), legacy: legacy}
	got, err := m.Capture(context.Background(), "")
	if err != nil || got[0].Identity != "google:owner" {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(m.host.Home, AntigravityAuthFile)); !os.IsNotExist(err) {
		t.Fatal("owner capture wrote token file")
	}
	if legacy.writes != 0 {
		t.Fatal("owner capture changed keyring")
	}
}

// testJWT builds an unsigned test token with a subject and expiry.
func testJWT(sub string, exp int64) string {
	raw, _ := json.Marshal(map[string]any{"sub": sub, "exp": exp})
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".test-signature"
}

func testHost(t *testing.T, env map[string]string) Host {
	t.Helper()
	return Host{Home: t.TempDir(), Env: func(k string) string { return env[k] }}
}

func testWrite(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
