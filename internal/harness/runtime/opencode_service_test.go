package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	agentstore "dyna-agent/internal/store"
)

func TestOpenCodeCachedGenerationUsesManagedService(t *testing.T) {
	dir := t.TempDir()
	manifest := nativeGeneration{Version: 2, Args: []string{"--standalone"}, Env: map[string]string{}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "native-generation.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeGeneration(filepath.Join(dir, "private-home.json"), privateGenerationHome{Version: 1, Home: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	generation, err := loadNativeGeneration("opencode", dir, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(generation.Args) != 0 {
		t.Fatalf("cached generation still launches a competing SQLite writer: %v", generation.Args)
	}
}

func TestOpenCodePreparationLeavesServerToSessionHost(t *testing.T) {
	a := testAccounts(t)
	scope, err := a.HarnessHome("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	if err = home.Ensure(scope); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1 $2" in
"service restart")
 case "$(cat "$OPENCODE_CONFIG_DIR/service.json")" in
  *\"port\":*) ;;
  *) echo restart-without-port >> "$OPENCODE_CONFIG_DIR/order"; exit 1 ;;
 esac
 mkdir -p "$XDG_STATE_HOME/opencode"
 echo '{"pid":123}' > "$XDG_STATE_HOME/opencode/service.json"
 echo "restart:$OPENCODE_CONFIG" >> "$OPENCODE_CONFIG_DIR/order"
 ;;
"auth list")
 if [ -f "$OPENCODE_CONFIG_DIR/require-generation" ]; then
  if [ ! -f "$OPENCODE_CONFIG_DIR/.dyna-service-generation.json" ]; then
   echo probe-before-generation >> "$OPENCODE_CONFIG_DIR/order"
   exit 1
  fi
  echo probe >> "$OPENCODE_CONFIG_DIR/order"
 fi
 echo '[{"id":"opencode-go","connections":[{"type":"credential","id":"fixture"}]}]'
 ;;
*) exit 1 ;;
esac
`
	bundles := manager(t)
	bundles.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil {
			return err
		}
		// Supply the actual platform entry points used by the OpenCode recipe.
		for _, platform := range []string{"linux-x64", "linux-arm64", "darwin-x64", "darwin-arm64"} {
			exe := filepath.Join(args[4], "lib", "node_modules", "@opencode", "cli", "node_modules", "@opencode", "cli-"+platform, "bin", "opencode")
			if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
				return err
			}
			if err := fsutil.WriteExecutable(exe, []byte(script), 0700); err != nil {
				return err
			}
		}
		return nil
	}
	bundle := install(t, bundles, "opencode", "2.0.32")
	manager, _, err := a.scopedManager("opencode", scope, bundle.Executable)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := manager.Capture(context.Background(), "")
	if err != nil || len(captured) != 1 {
		t.Fatalf("fixture capture: %v", err)
	}
	account, err := a.Create("opencode", "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.transaction(func(state *accountstore.State) error {
		entry := state.Find(account.ID)
		entry.Identity, entry.Integration, entry.NeedsLogin = captured[0].Identity, captured[0].Integration, false
		return writeAccountLogin(account.Home, captured[0])
	}, true); err != nil {
		t.Fatal(err)
	}
	if err = a.setActive("opencode", account.ID); err != nil {
		t.Fatal(err)
	}
	// Existing accounts can predate managed services. Remove the service config
	// that fixture capture created so readiness cannot hide missing bootstrap.
	if err = os.Remove(filepath.Join(scope, "service.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(scope, "require-generation"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Bundles: bundles, Accounts: a, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(agentstore.DataDir(), "harnesses", "generations")}
	selected, _, lease, err := r.PrepareLeased(context.Background(), profile.Profile{Name: "fixture", Harness: "opencode", RuntimeVersion: "2.0.32"}, nil)
	if err != nil {
		t.Fatal("cold service was probed before selecting its generation:", err)
	}
	defer lease.Close()
	if _, err := os.Stat(filepath.Join(scope, "order")); !os.IsNotExist(err) {
		t.Fatalf("preparation started a background service: %v", err)
	}
	if selected.Env["OPENCODE_CONFIG"] == "" {
		t.Fatal("selected generation has no config")
	}
}
