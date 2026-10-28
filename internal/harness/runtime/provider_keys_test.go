package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
"auth list") printf '['; sep=; while read p id; do printf '%s{"id":"%s","name":"Name-%s","connections":[{"type":"credential","id":"%s","method":"key"}]}' "$sep" "$p" "$p" "$p" "$id"; sep=,; done < "$db"; echo ']' ;;
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
	r := &Runtime{Bundles: m, Accounts: testAccounts(t), privateRecommendations: func(context.Context) []RecommendedVersion { return []RecommendedVersion{} }}
	installFakePiAuth(t, r.Accounts)
	return r
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// fakePiAuthCommands answers Dyna's pi auth contract against the home's real
// login file, so tests run the new code path without a Pi process. It reads
// the file at every command, like Pi does under its own lock.
func fakePiAuthCommands(ctx context.Context, executable, home string, argv []string, stdin []byte) ([]byte, error) {
	if len(argv) < 2 || argv[0] != "auth" {
		return nil, errFakePiAuthCommand
	}
	path := filepath.Join(home, "auth.json")
	entries := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(raw, &entries) != nil {
			return nil, errFakePiAuthCommand
		}
	}
	switch argv[1] {
	case "list":
		if len(argv) != 3 || argv[2] != "--json" {
			return nil, errFakePiAuthCommand
		}
		return json.Marshal(entries)
	case "apply":
		if len(argv) != 2 {
			return nil, errFakePiAuthCommand
		}
		// Pi's apply contract: one JSON edit of set, remove and expect on
		// stdin, one read and one write, and the resulting entries on stdout.
		var op struct {
			Set    map[string]json.RawMessage  `json:"set"`
			Remove []string                    `json:"remove"`
			Expect *map[string]json.RawMessage `json:"expect"`
		}
		if json.Unmarshal(stdin, &op) != nil {
			return nil, errFakePiAuthCommand
		}
		if op.Expect != nil && !sameFixtureEntries(entries, *op.Expect) {
			return nil, errFakePiAuthChanged
		}
		for _, id := range op.Remove {
			delete(entries, id)
		}
		for id, entry := range op.Set {
			entries[id] = append(json.RawMessage(nil), entry...)
		}
		out, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, out, 0600); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, errFakePiAuthCommand
}

var errFakePiAuthCommand = os.ErrInvalid

var errFakePiAuthChanged = errors.New("fake pi: the login changed since it was read")

// sameFixtureEntries reports whether two entry sets hold the same JSON, so
// the fake's expect check compares content like Pi's parsed comparison.
func sameFixtureEntries(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for id, entry := range a {
		other, held := b[id]
		if !held {
			return false
		}
		var left, right interface{}
		if json.Unmarshal(entry, &left) != nil || json.Unmarshal(other, &right) != nil || !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

// installFakePiAuth routes every pi auth command of accounts through
// fakePiAuthCommands, so provider-key tests run no Pi process.
func installFakePiAuth(t *testing.T, accounts *Accounts) {
	t.Helper()
	accounts.PiExecutable = func() (string, error) { return "/fake/pi", nil }
	old := piAuthRunner
	piAuthRunner = fakePiAuthCommands
	t.Cleanup(func() { piAuthRunner = old })
}

// The whole key edit is one auth apply command, so the complete edit takes
// Pi's lock once and the secret rides on stdin, never in an argument list.
func TestPiAPIKeyEditRunsOneAuthApplyWithTheKeyOnStdin(t *testing.T) {
	r := providerKeysRuntime(t)
	var calls []string
	old := piAuthRunner
	piAuthRunner = func(ctx context.Context, executable, home string, argv []string, stdin []byte) ([]byte, error) {
		calls = append(calls, strings.Join(argv, " ")+" <<"+string(stdin))
		return json.Marshal(map[string]json.RawMessage{})
	}
	t.Cleanup(func() { piAuthRunner = old })

	account, err := r.Accounts.CreateProvider("pi", piProvider("zai"), store.KindAPIKey, "zai login")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Accounts.piEditAuth(context.Background(), account.Home, piAPIKeyEdit("zai", "sk-secret-123")); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want the list and one apply:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	if !strings.HasPrefix(calls[1], "auth apply <<") {
		t.Fatalf("the key edit did not run one auth apply:\n%s", calls[1])
	}
	if !strings.Contains(calls[1], `"set":{"zai":{"key":"sk-secret-123","type":"api_key"}}`) {
		t.Fatalf("the apply did not set the zai entry on stdin:\n%s", calls[1])
	}
	if strings.Contains(calls[0], "sk-secret-123") {
		t.Fatal("the API key appears outside the apply stdin")
	}
}

// An apply diffs against the login it finds under the lock, so an entry
// another process added between the list and the apply survives, and Dyna
// sees the login as the file now holds it.
func TestAnApplyKeepsEntriesAddedAfterTheList(t *testing.T) {
	r := providerKeysRuntime(t)
	account, err := r.Accounts.CreateProvider("pi", piProvider("zai"), store.KindAPIKey, "zai login")
	if err != nil {
		t.Fatal(err)
	}
	home := account.Home
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"zai":{"type":"api_key","key":"old"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	listed := false
	old := piAuthRunner
	piAuthRunner = func(ctx context.Context, executable, h string, argv []string, stdin []byte) ([]byte, error) {
		if argv[1] == "list" && !listed {
			listed = true
			// A token refresh writes its own entry between Dyna's list and apply.
			if err := os.WriteFile(filepath.Join(h, "auth.json"), []byte(`{"zai":{"type":"api_key","key":"old"},"zzz":{"type":"oauth","access":"new"}}`), 0600); err != nil {
				return nil, err
			}
		}
		return fakePiAuthCommands(ctx, executable, h, argv, stdin)
	}
	t.Cleanup(func() { piAuthRunner = old })

	result, err := r.Accounts.piEditAuth(context.Background(), home, piAPIKeyEdit("zai", "sk-new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result["zzz"]) != `{"type":"oauth","access":"new"}` {
		t.Fatalf("the entry added after the list did not survive: %s", result["zzz"])
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || !strings.Contains(string(raw), `"zzz"`) || !strings.Contains(string(raw), "sk-new") {
		t.Fatalf("login file after the apply: %s %v", raw, err)
	}
}

// A restore refuses while the login no longer holds what the failed edit
// wrote, so it never overwrites the token another process refreshed in the
// meantime, and it goes through unchanged when the state still matches.
func TestARestoreRefusesWhenTheLoginChangedAfterTheEdit(t *testing.T) {
	r := providerKeysRuntime(t)
	account, err := r.Accounts.CreateProvider("pi", piProvider("zai"), store.KindOAuth, "zai login")
	if err != nil {
		t.Fatal(err)
	}
	home := account.Home
	stale := map[string]json.RawMessage{"zai": json.RawMessage(`{"type":"oauth","access":"stale","refresh":"r","expires":1,"accountId":"w"}`)}
	before := []byte(`{"zai":{"type":"oauth","access":"old","refresh":"r","expires":1,"accountId":"w"}}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"zai":{"type":"oauth","access":"stale","refresh":"r","expires":1,"accountId":"w"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Accounts.restorePiAuth(context.Background(), home, before, true, stale); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || !strings.Contains(string(raw), `"access":"old"`) {
		t.Fatalf("the matching restore did not put the earlier login back: %s %v", raw, err)
	}

	refreshed := []byte(`{"zai":{"type":"oauth","access":"fresh","refresh":"r","expires":1,"accountId":"w"}}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), refreshed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Accounts.restorePiAuth(context.Background(), home, before, true, stale); err == nil {
		t.Fatal("the restore of a changed login succeeded, want a refusal")
	}
	raw, err = os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(raw) != string(refreshed) {
		t.Fatalf("the refused restore changed the login: %s %v", raw, err)
	}
}

func TestWritePiAccountAuthReplacesInOneApply(t *testing.T) {
	r := providerKeysRuntime(t)
	account, err := r.Accounts.CreateProvider("pi", piProvider("keep"), store.KindAPIKey, "keep login")
	if err != nil {
		t.Fatal(err)
	}
	home := account.Home
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"drop":{"type":"api_key","key":"d"},"keep":{"type":"api_key","key":"old"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	old := piAuthRunner
	piAuthRunner = func(ctx context.Context, executable, h string, argv []string, stdin []byte) ([]byte, error) {
		calls = append(calls, strings.Join(argv, " ")+" <<"+string(stdin))
		return fakePiAuthCommands(ctx, executable, h, argv, stdin)
	}
	t.Cleanup(func() { piAuthRunner = old })

	if err := r.writePiAccountAuth(context.Background(), account, map[string]json.RawMessage{"keep": json.RawMessage(`{"type":"api_key","key":"new"}`)}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want the list and one apply:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	if !strings.Contains(calls[1], `"remove":["drop"]`) {
		t.Fatalf("the unwanted provider was not removed:\n%s", calls[1])
	}
	if !strings.Contains(calls[1], `"set":{"keep":{"type":"api_key","key":"new"}}`) {
		t.Fatalf("the wanted provider was not set:\n%s", calls[1])
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(raw) != `{"keep":{"type":"api_key","key":"new"}}` {
		t.Fatalf("login file after the replacement: %s %v", raw, err)
	}
}

func TestPiProviderKeyCreatesAndActivatesAnAccount(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.4.0-dyna.8")
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
