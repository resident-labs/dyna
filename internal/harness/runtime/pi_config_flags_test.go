package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	harnessmgr "dyna-agent/internal/harness/config"
)

// flagValue returns the argument that follows name in args, or "" when the
// flag is absent.
func flagValue(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// Each Pi generation passes its own settings, MCP servers and, when the owner
// defined models, the shared model file per invocation. Two accounts a worker
// switches between then cannot overwrite each other's files in one agent
// directory, and no account home holds files Dyna manages.
func TestPiGenerationsPointConfigFlagsAtTheirOwnDirs(t *testing.T) {
	r, base := runtimeFixture(t, "pi")
	noInstalledDyna(t)
	firstOut, pinOne, err := r.Prepare(context.Background(), base, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := firstHome(t, r)
	second := signedPiAccount(t, r, store.ProviderZAI, "second-key")
	if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: "flagtest", MCPDefinition: harnessmgr.MCPDefinition{Command: "true"}, EnabledHarnesses: []string{"pi"}}); err != nil {
		t.Fatal(err)
	}
	secondOut, pinTwo, err := r.Prepare(context.Background(), base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pinOne.Generation == pinTwo.Generation {
		t.Fatal("a changed config reused the first generation")
	}
	dirTwo := filepath.Dir(flagValue(secondOut.ExtraArgs, "--settings"))
	if dirTwo != pinTwo.Generation {
		t.Fatalf("second settings flag outside its own generation: %q", dirTwo)
	}
	dirOne := pinOne.Generation
	if s := flagValue(firstOut.ExtraArgs, "--settings"); s != filepath.Join(dirOne, "settings.json") {
		t.Fatalf("settings flag = %q", s)
	}
	if m := flagValue(firstOut.ExtraArgs, "--mcp-config"); m != filepath.Join(dirOne, "pi-mcp.json") {
		t.Fatalf("mcp flag = %q", m)
	}
	if flagValue(firstOut.ExtraArgs, "--settings") == flagValue(secondOut.ExtraArgs, "--settings") {
		t.Fatal("both generations pointed at one settings file")
	}
	for _, home := range []string{first, second.Home} {
		for _, name := range []string{"settings.json", "mcp.json", "models.json", ".dyna-generation"} {
			if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
				t.Fatalf("account home gained %s", name)
			}
		}
	}
}

// firstHome returns the active Pi account's home before a second sign-in.
func firstHome(t *testing.T, r *Runtime) string {
	t.Helper()
	account, err := r.Accounts.Active(store.ProviderZAI, "pi")
	if err != nil {
		t.Fatal(err)
	}
	return account.Home
}

// The owner's shared models.json reaches a Pi process through --models-file;
// without the flag Pi would read the account home, which Dyna no longer
// manages.
func TestPiConfigArgsJoinsSharedModelsFile(t *testing.T) {
	home := t.TempDir()
	gen := filepath.Join(home, "gen-dir")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	args := piConfigArgs(home, filepath.Join(gen, "settings.json"))
	if d := flagValue(args, "--session-dir"); d != filepath.Join(home, "sessions") {
		t.Fatalf("session dir = %q", d)
	}
	if s := flagValue(args, "--settings"); s != filepath.Join(gen, "settings.json") {
		t.Fatalf("settings flag = %q", s)
	}
	if m := flagValue(args, "--mcp-config"); m != filepath.Join(gen, "pi-mcp.json") {
		t.Fatalf("mcp flag = %q", m)
	}
	if m := flagValue(args, "--models-file"); m != "" {
		t.Fatalf("models flag without a shared file = %q", m)
	}
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args = piConfigArgs(home, filepath.Join(gen, "settings.json"))
	if m := flagValue(args, "--models-file"); m != filepath.Join(home, "models.json") {
		t.Fatalf("shared models file not joined: %q", m)
	}
}

// Dyna disables Pi's own turn retry in the settings file it passes per
// invocation; the startup RPC it once sent is gone.
func TestPiGenerationSettingsDisableOwnRetry(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	noInstalledDyna(t)
	out, _, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := flagValue(out.ExtraArgs, "--settings")
	if s == "" {
		t.Fatal("generation passed no settings file")
	}
	raw, err := os.ReadFile(s)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Retry struct {
			Enabled *bool
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Retry.Enabled == nil || *doc.Retry.Enabled {
		t.Fatal("generation settings left Pi's own retry enabled")
	}
}
