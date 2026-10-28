package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
// login file, so tests run the new code path without a Pi process.
func fakePiAuthCommands(ctx context.Context, executable, home string, argv []string, stdin []byte) ([]byte, error) {
	if argv[0] != "auth" || (len(argv) != 3 && len(argv) != 4) {
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
	case "set", "remove":
		if len(argv) != 4 || argv[2] != "--provider" {
			return nil, errFakePiAuthCommand
		}
		if argv[1] == "set" {
			entries[argv[3]] = append(json.RawMessage(nil), stdin...)
		} else {
			delete(entries, argv[3])
		}
		out, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		return nil, os.WriteFile(path, out, 0600)
	}
	return nil, errFakePiAuthCommand
}

var errFakePiAuthCommand = os.ErrInvalid

// installFakePiAuth routes every pi auth command of accounts through
// fakePiAuthCommands, so provider-key tests run no Pi process.
func installFakePiAuth(t *testing.T, accounts *Accounts) {
	t.Helper()
	accounts.PiExecutable = func() (string, error) { return "/fake/pi", nil }
	old := piAuthRunner
	piAuthRunner = fakePiAuthCommands
	t.Cleanup(func() { piAuthRunner = old })
}

func TestPiAPIKeyEditRunsAuthSetWithTheKeyOnStdin(t *testing.T) {
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

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "auth set --provider zai") || !strings.Contains(joined, "<<"+`{"key":"sk-secret-123","type":"api_key"}`) {
		t.Fatalf("the key edit did not run auth set with the entry on stdin:\n%s", joined)
	}
	if strings.Contains(strings.Join(calls, " "), "sk-secret-123 ") {
		t.Fatal("the API key appears in an argument list")
	}
}

func TestWritePiAccountAuthRemovesUnwantedAndSetsWanted(t *testing.T) {
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

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "auth remove --provider drop") {
		t.Fatalf("the unwanted provider was not removed:\n%s", joined)
	}
	if !strings.Contains(joined, "auth set --provider keep <<"+`{"type":"api_key","key":"new"}`) {
		t.Fatalf("the wanted provider was not set:\n%s", joined)
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
