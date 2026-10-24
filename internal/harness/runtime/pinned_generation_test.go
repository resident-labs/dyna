package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

// A session keeps its generation across turns. When an older pin names a Dyna
// command that no longer runs, the next turn must rebuild the generation, or
// its Codex or Claude thread starts an MCP server that cannot handshake and the
// turn has no Dyna tools.
func kimiRuntimeFixture(t *testing.T) (*Runtime, profile.Profile) {
	t.Helper()
	accounts := testAccounts(t)
	n, err := accounts.Create("kimi-code", "private")
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`default_model = "kimi-code/k3"
[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
`)
	token := []byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_at":2000000000,"scope":"coding","token_type":"Bearer","expires_in":3600}`)
	home, err := accounts.HarnessHome("kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "credentials", "kimi-code.json"), token, 0600); err != nil {
		t.Fatal(err)
	}
	bundles := manager(t)
	bundle := install(t, bundles, "kimi-code", "2.3.1")
	flow, err := accounts.BeginLogin(context.Background(), n.ID, bundle.Executable)
	if err != nil {
		t.Fatal(err)
	}
	n, err = flow.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Bundles: bundles, Accounts: accounts, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(store.DataDir(), "harnesses", "generations")}
	return r, profile.Profile{Name: "private", Harness: n.Harness}
}

func TestKimiPrepareReappliesToolPolicyAfterAccountChange(t *testing.T) {
	r, p := kimiRuntimeFixture(t)
	_, pin, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	home, err := r.Accounts.HarnessHome("kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	// Account activation restored a valid saved config without Dyna's policy.
	if err := os.WriteFile(configPath, []byte(`default_model = "kimi-code/k3"
[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
`), 0600); err != nil {
		t.Fatal(err)
	}
	_, resumed, err := r.Prepare(context.Background(), p, &pin)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Generation != pin.Generation {
		t.Fatal("a valid generation pin was rebuilt")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Agent", "Bash", "TaskList"} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("resumed config lacks denied tool %s: %s", name, raw)
		}
	}
	entries, err := os.ReadDir(filepath.Join(r.Generations, ".views"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Kimi created mutable generation-view JSON records: %v", entries)
	}
}

func TestPrepareRebuildsPinWhoseDynaCommandIsGone(t *testing.T) {
	r, p := runtimeFixture(t, "codex")
	gone := filepath.Join(t.TempDir(), "gone")
	dead := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(dead, []byte("#!/bin/sh\nexec '"+gone+"' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p.Env = map[string]string{"DYNA_BIN": dead}
	if _, pin, err := r.Prepare(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	} else if pin.Generation == "" {
		t.Fatal("prepare pinned no generation")
	}
	_, first, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if harnesshome.Launches(dead) {
		t.Fatal("dead bridge reported as runnable")
	}

	live := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(live, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p.Env = map[string]string{"DYNA_BIN": live}
	_, rebuilt, err := r.Prepare(context.Background(), p, &first)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Generation == first.Generation {
		t.Fatal("turn reused a generation whose Dyna command is gone")
	}
	gen, err := loadNativeGeneration("codex", rebuilt.Generation, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if !harnesshome.Launches(gen.CLI) || !strings.Contains(strings.Join(gen.Args, " "), gen.CLI) {
		t.Fatalf("rebuilt generation still names an unusable Dyna command: %q", gen.CLI)
	}
}
