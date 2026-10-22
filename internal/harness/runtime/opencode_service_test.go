package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	agentstore "dyna-agent/internal/store"
)

func TestOpenCodeServiceGenerationDrainsClients(t *testing.T) {
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
	env := home.Roots(scope)
	env["OPENCODE_CONFIG_DIR"] = scope
	env["OPENCODE_CONFIG"] = filepath.Join(scope, "gen-one", "opencode.json")
	env["MCP_TOKEN"] = "must-not-be-persisted"
	exe := filepath.Join(t.TempDir(), "opencode")
	script := `#!/bin/sh
mkdir -p "$XDG_STATE_HOME/opencode"
echo '{"pid":123}' > "$XDG_STATE_HOME/opencode/service.json"
echo "$OPENCODE_CONFIG" >> "$OPENCODE_CONFIG_DIR/restarts"
`
	if err = fsutil.WriteExecutable(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Env: env}
	pin := Pin{Generation: "gen-one", Bundle: Bundle{Executable: exe}}
	first, err := prepareOpenCodeService(context.Background(), p, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	config, err := readOpenCodeServiceConfig(scope)
	if err != nil || openCodeServicePort(config) <= 0 || openCodeServicePort(config) > 65535 {
		t.Fatalf("service started without a private port: %v", err)
	}
	second, err := prepareOpenCodeService(context.Background(), p, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pin.Generation = "gen-two"
	env["OPENCODE_CONFIG"] = filepath.Join(scope, "gen-two", "opencode.json")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if lease, err := prepareOpenCodeService(ctx, p, pin); !errors.Is(err, context.DeadlineExceeded) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("generation changed while clients still used the service: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(scope, "restarts"))
	if err != nil || strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("same-generation clients restarted their service: %q, %v", raw, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := prepareOpenCodeService(context.Background(), p, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	raw, err = os.ReadFile(filepath.Join(scope, "restarts"))
	if err != nil || strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("drained service did not restart: %q, %v", raw, err)
	}
	config, err = readOpenCodeServiceConfig(scope)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(config)
	if strings.Contains(string(raw), env["MCP_TOKEN"]) || !strings.Contains(string(raw), "gen-two") {
		t.Fatal("service did not pin config without storing resolved tokens")
	}
}

// Enable only with an installed private bundle. All subprocesses receive an
// empty environment plus scratch roots; no owner's credentials are read.
func TestOpenCodeRealManagedServiceConcurrentClients(t *testing.T) {
	exe := os.Getenv("OPENCODE_INTEGRATION_BINARY")
	if exe == "" {
		t.Skip("set OPENCODE_INTEGRATION_BINARY to the private v2 bundle")
	}
	a := testAccounts(t)
	scope, err := a.HarnessHome("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if err = home.Ensure(scope); err != nil {
		t.Fatal(err)
	}
	if err = ensureOpenCodeServicePort(scope); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PATH")
	env := map[string]string{}
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		env[key] = ""
		t.Setenv(key, "")
	}
	t.Setenv("PATH", path)
	for key, value := range home.Roots(scope) {
		env[key] = value
	}
	env["PATH"] = path
	env["PWD"] = env["HOME"]
	env["OPENCODE_TEST_HOME"] = env["HOME"]
	env["OPENCODE_CONFIG_DIR"] = scope
	env["OPENCODE_DB"] = filepath.Join(scope, "opencode.db")
	env["OPENCODE_DISABLE_AUTOUPDATE"] = "1"
	env["OPENCODE_DISABLE_MODELS_FETCH"] = "1"
	env["OPENCODE_CONFIG_CONTENT"] = home.OpenCodeEmptyConfigContent
	env["OPENCODE_CONFIG"] = filepath.Join(scope, "fixture-config.json")
	if err = os.WriteFile(env["OPENCODE_CONFIG"], []byte(`{"username":"SERVICE_STARTUP_CANARY","update":"disable","mcp":{"servers":{"audit":{"type":"local","command":["never-run-audit-mcp"],"disabled":true}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var commandEnv []string
	for key, value := range env {
		commandEnv = append(commandEnv, key+"="+value)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "service", "stop")
		cmd.Env = commandEnv
		cmd.Dir = env["HOME"]
		if err := cmd.Run(); err != nil {
			t.Errorf("stop private test service: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	lease, err := prepareOpenCodeService(ctx, profile.Profile{Env: env}, Pin{Generation: "fixture", Bundle: Bundle{Executable: exe}})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	// A conflicting client-only layer cannot change service-owned settings.
	probe := exec.CommandContext(ctx, exe, "api", "GET", "/api/config")
	probe.Env = append(append([]string{}, commandEnv...), `OPENCODE_CONFIG_CONTENT={"username":"CLIENT_ONLY_CANARY"}`)
	probe.Dir = env["HOME"]
	configOutput, probeErr := probe.CombinedOutput()
	if probeErr != nil || !strings.Contains(string(configOutput), "SERVICE_STARTUP_CANARY") || strings.Contains(string(configOutput), "CLIENT_ONLY_CANARY") {
		t.Fatalf("service config boundary: %s, %v", configOutput, probeErr)
	}
	var effective any
	if err = json.Unmarshal(configOutput, &effective); err != nil {
		t.Fatal(err)
	}
	normalized, _ := json.Marshal(effective)
	for _, want := range []string{`"update":"disable"`, `"disabled":true`, "never-run-audit-mcp"} {
		if !strings.Contains(string(normalized), want) {
			t.Fatalf("service config lost %s: %s", want, normalized)
		}
	}
	manager, ok := native.For("opencode", native.Host{OpenCodeService: true, Home: env["HOME"], Executable: exe, Env: func(key string) string { return env[key] }})
	if !ok {
		t.Fatal("missing native adapter")
	}
	var workers sync.WaitGroup
	sessions := make(chan string, 6)
	for range 6 {
		workers.Go(func() {
			if _, err := manager.Capture(ctx, ""); !errors.Is(err, native.ErrNotSignedIn) {
				t.Errorf("empty private probe: %v", err)
				return
			}
			cmd := exec.CommandContext(ctx, exe, "run", "--format", "json", "--model", "fixture-invalid/model", "fixture")
			cmd.Env = commandEnv
			cmd.Dir = env["HOME"]
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			_ = cmd.Run()
			var event struct {
				SessionID string `json:"sessionID"`
				Error     struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil || event.SessionID == "" || event.Error.Type != "provider.no-route" {
				t.Errorf("private turn failed before its expected invalid model: %s (%v)", output.Bytes(), err)
				return
			}
			sessions <- event.SessionID
		})
	}
	workers.Wait()
	close(sessions)
	seen := map[string]bool{}
	for id := range sessions {
		if seen[id] {
			t.Errorf("shared session %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 6 {
		t.Fatalf("only %d private turn clients created independent sessions", len(seen))
	}
}

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

func TestOpenCodeColdServiceStartsWithSelectedGeneration(t *testing.T) {
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
	selected, _, lease, err := r.PrepareLeased(context.Background(), profile.Profile{Name: "fixture", Harness: "opencode", RuntimeVersion: "2.0.32", RuntimeAccount: account.ID}, nil)
	if err != nil {
		t.Fatal("cold service was probed before selecting its generation:", err)
	}
	defer lease.Close()
	raw, err := os.ReadFile(filepath.Join(scope, "order"))
	if err != nil {
		t.Fatal(err)
	}
	want := "restart:" + selected.Env["OPENCODE_CONFIG"] + "\nprobe\n"
	if string(raw) != want {
		t.Fatalf("cold service did not start exactly once with its selected config: got %q, want %q", raw, want)
	}
}
