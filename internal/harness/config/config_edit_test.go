package config

import (
	"bytes"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/store"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func editManager(t *testing.T) (*ConfigManager, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	account := filepath.Join(root, "account")
	generations := filepath.Join(root, "generations")
	for _, p := range []string{account, generations} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return &ConfigManager{Home: home, Path: filepath.Join(root, "catalog.json")}, account, generations
}
func mustEdit(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func fileBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	mustEdit(t, e)
	return b
}
func TestCatalogEditsPreservePersonalFiles(t *testing.T) {
	m, _, _ := editManager(t)
	canaries := map[string][]byte{}
	for _, p := range []string{".claude.json", ".codex/config.toml", ".agents/skills/canary/SKILL.md"} {
		full := filepath.Join(m.Home, p)
		mustEdit(t, os.MkdirAll(filepath.Dir(full), 0700))
		b := []byte("personal canary " + p)
		mustEdit(t, os.WriteFile(full, b, 0600))
		canaries[full] = b
	}
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tool", MCPDefinition: MCPDefinition{Command: "node"}, EnabledHarnesses: []string{"claude-code"}}))
	if err := m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tool", MCPDefinition: MCPDefinition{Command: "other"}}); err == nil {
		t.Fatal("duplicate accepted")
	}
	mustEdit(t, m.SetEnabled(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", "codex", true))
	mustEdit(t, m.Remove(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", "claude"))
	c, e := m.Read()
	mustEdit(t, e)
	if len(c.MCP) != 1 || strings.Join(c.MCP[0].EnabledHarnesses, ",") != "codex" || c.MCP[0].Command != "node" {
		t.Fatalf("definition or enablement lost: %+v", c)
	}
	mustEdit(t, m.Remove(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", ""))
	c, e = m.Read()
	mustEdit(t, e)
	if len(c.MCP) != 0 {
		t.Fatal("remove failed")
	}
	for p, b := range canaries {
		if !bytes.Equal(fileBytes(t, p), b) {
			t.Fatalf("personal file changed: %s", p)
		}
	}
}
func TestCatalogRejectsUnsafeDefinitions(t *testing.T) {
	m, _, _ := editManager(t)
	tests := []ManagedMCP{
		{Name: "../escape", MCPDefinition: MCPDefinition{Command: "node"}},
		{Name: "secret", MCPDefinition: MCPDefinition{URL: "https://x.invalid/?token=live-canary"}},
		{Name: "secret", MCPDefinition: MCPDefinition{URL: "https://u:live-canary@x.invalid"}},
		{Name: "secret", MCPDefinition: MCPDefinition{Command: "node", Args: []string{"--api-key", "live-canary"}}},
		{Name: "secret", MCPDefinition: MCPDefinition{Command: "https://u:live-canary@x.invalid"}},
		{Name: "secret", MCPDefinition: MCPDefinition{Command: "node", EnvKeys: []string{"TOKEN=live-canary"}}},
		{Name: "secret", MCPDefinition: MCPDefinition{Command: "node"}, EnabledHarnesses: []string{"invalid"}},
		{Name: "secret", MCPDefinition: MCPDefinition{}},
		{Name: "secret", MCPDefinition: MCPDefinition{Command: "node", EnvKeys: []string{"HOME"}}},
		{Name: "secret", MCPDefinition: MCPDefinition{Command: "node", EnvKeys: []string{"XDG_CONFIG_HOME"}}},
	}
	for _, x := range tests {
		err := m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), x)
		if err == nil {
			t.Fatalf("unsafe accepted: %+v", x)
		}
		if strings.Contains(err.Error(), "live-canary") {
			t.Fatalf("error leaked value: %v", err)
		}
	}
	if _, e := os.Stat(m.Path); !os.IsNotExist(e) {
		t.Fatal("rejected definitions created catalog")
	}
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "safe", MCPDefinition: MCPDefinition{URL: "https://x.invalid/mcp"}}))
	// A command path that names a credentials folder is not a secret flag.
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "pathed", MCPDefinition: MCPDefinition{Command: "/srv/credentials/bin/dyna", Args: []string{"mcp", "shell"}}}))
	if bytes.Contains(fileBytes(t, m.Path), []byte("live-canary")) {
		t.Fatal("secret in catalog")
	}
}
func TestGeneratePrivateImmutableConfigAndEnvironment(t *testing.T) {
	m, account, root := editManager(t)
	personal := filepath.Join(m.Home, ".claude.json")
	mustEdit(t, os.WriteFile(personal, []byte("canary"), 0600))
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tool", MCPDefinition: MCPDefinition{Command: "node", Args: []string{"quotes\"slash\\\n雪"}, EnvKeys: []string{"TEST_GENERATION_SECRET"}}, EnabledHarnesses: []string{"claude", "codex"}}))
	lookup := func(key string) (string, bool) { return "secret-value-canary", key == "TEST_GENERATION_SECRET" }
	claude, e := m.GenerateWithEnv("claude-code", account, root, lookup)
	mustEdit(t, e)
	codex, e := m.GenerateWithEnv("codex", account, root, lookup)
	mustEdit(t, e)
	if claude.ID == codex.ID || len(codex.PartialReasons) == 0 {
		t.Fatal("generation identity or partial reason missing")
	}
	if reasons := strings.Join(codex.PartialReasons, " "); strings.Contains(reasons, "~/.agents") {
		t.Fatalf("Codex runs with a private HOME; stale personal skill reason: %s", reasons)
	}
	var native struct {
		Servers map[string]struct {
			Command string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	b := fileBytes(t, filepath.Join(claude.Directory, "mcp.json"))
	mustEdit(t, json.Unmarshal(b, &native))
	if native.Servers["tool"].Env["TEST_GENERATION_SECRET"] != "${TEST_GENERATION_SECRET}" || claude.Env["TEST_GENERATION_SECRET"] != "secret-value-canary" {
		t.Fatal("Claude missing env value")
	}
	var parsed struct {
		MCP map[string]struct {
			Command string
			Args    []string
			EnvVars []string `toml:"env_vars"`
		} `toml:"mcp_servers"`
	}
	mustEdit(t, toml.Unmarshal([]byte(codex.Args[1]), &parsed))
	for i := 0; i < len(codex.Args); i += 2 {
		if codex.Args[i] != "-c" || i+1 >= len(codex.Args) {
			t.Fatal("invalid Codex override argument pair")
		}
		var assignment map[string]any
		mustEdit(t, toml.Unmarshal([]byte(codex.Args[i+1]), &assignment))
		if value, ok := assignment["cli_auth_credentials_store"]; ok && value != "file" {
			t.Fatalf("invalid auth storage value: %v", value)
		}
		if value, ok := assignment["check_for_update_on_startup"]; ok && value != false {
			t.Fatalf("invalid updater value: %v", value)
		}
	}
	if parsed.MCP["tool"].Command != "node" || parsed.MCP["tool"].Args[0] != "quotes\"slash\\\n雪" {
		t.Fatalf("invalid native TOML: %+v", parsed)
	}
	if strings.Contains(strings.Join(codex.Args, " "), "secret-value-canary") || codex.Env["TEST_GENERATION_SECRET"] != "secret-value-canary" {
		t.Fatal("Codex secret transport unsafe")
	}
	mustEdit(t, m.Remove(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", ""))
	newer, e := m.Generate("claude", account, root)
	mustEdit(t, e)
	if !bytes.Equal(fileBytes(t, filepath.Join(claude.Directory, "mcp.json")), b) {
		t.Fatal("old generation changed")
	}
	if bytes.Contains(fileBytes(t, filepath.Join(newer.Directory, "mcp.json")), []byte("tool")) {
		t.Fatal("removed definition retained")
	}
	if bytes.Contains(fileBytes(t, m.Path), []byte("secret-value-canary")) {
		t.Fatal("secret stored in catalog")
	}
	if string(fileBytes(t, personal)) != "canary" {
		t.Fatal("personal file changed")
	}
	files, e := os.ReadDir(account)
	mustEdit(t, e)
	if len(files) != 1 || files[0].Name() != "skills" || !files[0].IsDir() {
		t.Fatal("account writes must contain only the shared Codex skill view")
	}
	mustEdit(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("public generation path: %s", p)
		}
		return nil
	}))
}
func TestGenerationSafeErrorsAndPaths(t *testing.T) {
	m, account, root := editManager(t)
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tool", MCPDefinition: MCPDefinition{Command: "node", EnvKeys: []string{"MISSING_KEY"}}, EnabledHarnesses: []string{"claude"}}))
	_, err := m.GenerateWithEnv("claude", account, root, func(string) (string, bool) { return "unavailable-secret-value", false })
	if err == nil || err.Error() != "missing environment key MISSING_KEY" {
		t.Fatalf("unsafe missing key error: %v", err)
	}
	for _, paths := range [][2]string{{m.Home, root}, {account, m.Home}, {account, account}, {"relative", root}} {
		if _, e := m.Generate("claude", paths[0], paths[1]); e == nil {
			t.Fatalf("unsafe paths accepted: %v", paths)
		}
	}
	personal := filepath.Join(m.Home, ".codex")
	mustEdit(t, os.Mkdir(personal, 0700))
	if _, e := m.Generate("codex", account, personal); e == nil {
		t.Fatal("personal config path accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	mustEdit(t, os.Symlink(root, link))
	if _, e := m.Generate("codex", account, link); e == nil {
		t.Fatal("symlink accepted")
	}
	mustEdit(t, os.Chmod(root, 0755))
	if _, e := m.Generate("codex", account, root); e == nil {
		t.Fatal("public directory accepted")
	}
}
func TestConcurrentGenerations(t *testing.T) {
	m, account, root := editManager(t)
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tool", MCPDefinition: MCPDefinition{Command: "node"}, EnabledHarnesses: []string{"claude", "codex"}}))
	var wg sync.WaitGroup
	directories := make(chan string, 16)
	for i := range 16 {
		wg.Go(func() {
			h := "claude"
			if i%2 == 1 {
				h = "codex"
			}
			g, e := m.Generate(h, account, root)
			if e != nil {
				t.Error(e)
				return
			}
			directories <- g.Directory
		})
	}
	wg.Wait()
	close(directories)
	seen := map[string]bool{}
	for d := range directories {
		if seen[d] {
			t.Fatal("generation overwritten")
		}
		seen[d] = true
		if len(fileBytes(t, filepath.Join(d, "mcp.json"))) == 0 {
			t.Fatal("empty generation")
		}
	}
	if len(seen) != 16 {
		t.Fatalf("got %d generations", len(seen))
	}
}
func TestSkillGenerationCopiesAndRejectsLinks(t *testing.T) {
	m, account, root := editManager(t)
	source := t.TempDir()
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("original skill"), 0600))
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "review", Source: source, EnabledHarnesses: []string{"claude", "codex"}}))
	g, e := m.Generate("claude", account, root)
	mustEdit(t, e)
	copied := filepath.Join(g.Directory, "plugin", "skills", "review", "SKILL.md")
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("new skill"), 0600))
	if string(fileBytes(t, copied)) != "original skill" {
		t.Fatal("skill generation is mutable")
	}
	if !strings.Contains(strings.Join(g.Args, " "), "--plugin-dir") {
		t.Fatal("plugin load flag missing")
	}

	mustEdit(t, os.Symlink(filepath.Join(source, "SKILL.md"), filepath.Join(source, "link")))
	if _, e := m.Generate("claude", account, root); e == nil {
		t.Fatal("mutable source link accepted")
	}
	entries, e := os.ReadDir(root)
	mustEdit(t, e)
	if len(entries) != 1 {
		t.Fatal("failed generation was retained")
	}
}

func TestLoadGenerationRetainsFilesAndResolvesCurrentKeys(t *testing.T) {
	m, account, root := editManager(t)
	mustEdit(t, m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "tool", MCPDefinition: MCPDefinition{Command: "node", EnvKeys: []string{"RESUME_SECRET"}}, EnabledHarnesses: []string{"claude", "codex"}}))
	for _, h := range []string{"claude", "codex"} {
		g, e := m.GenerateWithEnv(h, account, root, func(string) (string, bool) { return "first-secret-canary", true })
		mustEdit(t, e)

		serialized, e := json.Marshal(g)
		mustEdit(t, e)
		if bytes.Contains(serialized, []byte("first-secret-canary")) {
			t.Fatal("generation serialization leaked Env")
		}
		manifest := fileBytes(t, filepath.Join(g.Directory, "generation.json"))
		if bytes.Contains(manifest, []byte("first-secret-canary")) {
			t.Fatal("secret in generation metadata")
		}
		b := fileBytes(t, filepath.Join(g.Directory, "mcp.json"))
		if bytes.Contains(b, []byte("first-secret-canary")) {
			t.Fatal("secret value persisted in native config")
		}
		resumed, e := LoadGenerationWithEnv(g.Directory, func(string) (string, bool) { return "current-secret-canary", true })
		mustEdit(t, e)
		if resumed.ID != g.ID || strings.Join(resumed.Args, "\x00") != strings.Join(g.Args, "\x00") || resumed.Env["RESUME_SECRET"] != "current-secret-canary" {
			t.Fatal("resume changed pinned generation")
		}
		if !bytes.Equal(fileBytes(t, filepath.Join(g.Directory, "mcp.json")), b) {
			t.Fatal("resume rewrote generation")
		}
		if _, e := LoadGenerationWithEnv(g.Directory, nil); e == nil || e.Error() != "missing environment key RESUME_SECRET" {
			t.Fatalf("unsafe missing reference error: %v", e)
		}
	}
	mustEdit(t, m.Remove(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", ""))
	if ok, reason := NativeSkillCapability("codex"); !ok || !strings.Contains(reason, "not pinned per generation") {
		t.Fatal("unsupported native skill capability missing")
	}
	if ok, reason := NativeSkillCapability("claude"); !ok || reason != "" {
		t.Fatal("Claude plugin skills unsupported")
	}
}

func TestConcurrentCatalogEdits(t *testing.T) {
	m, _, _ := editManager(t)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			name := string(rune('a' + i))
			if e := m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: name, MCPDefinition: MCPDefinition{Command: "node"}}); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	c, e := m.Read()
	mustEdit(t, e)
	if len(c.MCP) != 12 {
		t.Fatalf("lost concurrent edit: %d", len(c.MCP))
	}
}

func TestCodexSharedSkillViewUpdatesWithoutResumeRollback(t *testing.T) {
	m, account, root := editManager(t)
	source := t.TempDir()
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("first skill"), 0600))
	personalSkill := filepath.Join(m.Home, ".agents", "skills", "personal", "SKILL.md")
	mustEdit(t, os.MkdirAll(filepath.Dir(personalSkill), 0700))
	mustEdit(t, os.WriteFile(personalSkill, []byte("personal canary"), 0600))
	auth := filepath.Join(account, "auth.json")
	mustEdit(t, os.WriteFile(auth, []byte("credential canary"), 0600))
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "review", Source: source, EnabledHarnesses: []string{"codex"}}))
	first, e := m.Generate("codex", account, root)
	mustEdit(t, e)
	view := filepath.Join(account, "skills")
	target, e := os.Readlink(filepath.Join(view, "review"))
	mustEdit(t, e)
	if target != filepath.Join(first.Directory, "skills", "review") {
		t.Fatal("native skill view does not reference generation copy")
	}
	copied := filepath.Join(first.Directory, "skills", "review", "SKILL.md")
	if string(fileBytes(t, filepath.Join(view, "review", "SKILL.md"))) != "first skill" {
		t.Fatal("native skill is missing")
	}
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("second skill"), 0600))
	second, e := m.Generate("codex", account, root)
	mustEdit(t, e)
	target, e = os.Readlink(filepath.Join(view, "review"))
	mustEdit(t, e)
	if target != filepath.Join(second.Directory, "skills", "review") {
		t.Fatal("native skill view did not advance")
	}
	if string(fileBytes(t, copied)) != "first skill" {
		t.Fatal("previous skill copy changed")
	}
	if string(fileBytes(t, filepath.Join(view, "review", "SKILL.md"))) != "second skill" {
		t.Fatal("native view did not load new copy")
	}
	resumed, e := LoadGeneration(first.Directory)
	mustEdit(t, e)
	target, e = os.Readlink(filepath.Join(view, "review"))
	mustEdit(t, e)
	if target != filepath.Join(second.Directory, "skills", "review") {
		t.Fatal("resume rolled back the shared skill view")
	}
	if !strings.Contains(strings.Join(resumed.PartialReasons, " "), "not pinned per generation") {
		t.Fatal("resume omitted the shared skill limitation")
	}
	mustEdit(t, m.SetEnabled(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "skill", "review", "codex", false))
	_, e = m.Generate("codex", account, root)
	mustEdit(t, e)
	entries, e := os.ReadDir(view)
	mustEdit(t, e)
	if len(entries) != 0 {
		t.Fatal("disabled skills remain active")
	}
	if string(fileBytes(t, personalSkill)) != "personal canary" || string(fileBytes(t, auth)) != "credential canary" {
		t.Fatal("generation changed personal skills or account credentials")
	}
	entries, e = os.ReadDir(account)
	mustEdit(t, e)
	if len(entries) != 2 {
		t.Fatal("temporary skill view staging was retained")
	}
}

func TestCodexFailedSkillGenerationPreservesSharedView(t *testing.T) {
	m, account, root := editManager(t)
	source := t.TempDir()
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("stable skill"), 0600))
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "review", Source: source, EnabledHarnesses: []string{"codex"}}))
	g, e := m.Generate("codex", account, root)
	mustEdit(t, e)
	mustEdit(t, os.Symlink(filepath.Join(source, "SKILL.md"), filepath.Join(source, "mutable-link")))
	if _, e := m.Generate("codex", account, root); e == nil {
		t.Fatal("mutable skill resource link accepted")
	}
	target, e := os.Readlink(filepath.Join(account, "skills", "review"))
	mustEdit(t, e)
	if target != filepath.Join(g.Directory, "skills", "review") {
		t.Fatal("failed generation changed native skill view")
	}
	entries, e := os.ReadDir(root)
	mustEdit(t, e)
	if len(entries) != 1 {
		t.Fatal("failed generation was retained")
	}
}

func TestCodexSkillViewRefusesExistingFile(t *testing.T) {
	m, account, root := editManager(t)
	view := filepath.Join(account, "skills")
	mustEdit(t, os.WriteFile(view, []byte("existing canary"), 0600))
	if _, e := m.Generate("codex", account, root); e == nil {
		t.Fatal("generation replaced an unmanaged skill path")
	}
	if string(fileBytes(t, view)) != "existing canary" {
		t.Fatal("generation changed unmanaged skill data")
	}
	entries, e := os.ReadDir(root)
	mustEdit(t, e)
	if len(entries) != 0 {
		t.Fatal("failed generation was retained")
	}
}

// Codex writes its own system skills into the view. They stay in the account
// home, and a Dyna skill never replaces an entry that the harness owns.
func TestCodexSkillViewKeepsHarnessEntries(t *testing.T) {
	m, account, root := editManager(t)
	source := t.TempDir()
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("dyna skill"), 0600))
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "review", Source: source, EnabledHarnesses: []string{"codex"}}))
	view := filepath.Join(account, "skills")
	owned := filepath.Join(view, "owned", "SKILL.md")
	mustEdit(t, os.MkdirAll(filepath.Dir(owned), 0700))
	mustEdit(t, os.WriteFile(owned, []byte("user skill"), 0600))
	g, e := m.Generate("codex", account, root)
	mustEdit(t, e)
	system := filepath.Join(view, ".system", "SKILL.md")
	mustEdit(t, os.MkdirAll(filepath.Dir(system), 0700))
	mustEdit(t, os.WriteFile(system, []byte("codex system skill"), 0600))
	if _, e := os.Lstat(filepath.Join(g.Directory, "skills", ".system")); !os.IsNotExist(e) {
		t.Fatal("harness write reached the generation", e)
	}
	_, e = m.Generate("codex", account, root)
	mustEdit(t, e)
	if string(fileBytes(t, system)) != "codex system skill" || string(fileBytes(t, owned)) != "user skill" {
		t.Fatal("publication changed harness-owned skills")
	}
}

func TestConcurrentCodexSkillViewsPublishCompleteGenerations(t *testing.T) {
	m, account, root := editManager(t)
	source := t.TempDir()
	mustEdit(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("complete skill"), 0600))
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "review", Source: source, EnabledHarnesses: []string{"codex"}}))
	_, e := m.Generate("codex", account, root)
	mustEdit(t, e)
	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			target, e := os.Readlink(filepath.Join(account, "skills", "review"))
			if e != nil {
				t.Error("atomic view disappeared:", e)
				return
			}
			b, e := os.ReadFile(filepath.Join(target, "SKILL.md"))
			if e != nil || string(b) != "complete skill" {
				t.Error("published incomplete skill generation:", e)
				return
			}
			if _, e := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(target)), "generation.json")); e != nil {
				t.Error("published generation before its manifest:", e)
				return
			}
		}
	})
	var writers sync.WaitGroup
	for range 12 {
		writers.Go(func() {
			if _, e := m.Generate("codex", account, root); e != nil {
				t.Error(e)
			}
		})
	}
	writers.Wait()
	close(stop)
	reader.Wait()
	entries, e := os.ReadDir(account)
	mustEdit(t, e)
	if len(entries) != 1 || entries[0].Name() != "skills" {
		t.Fatal("concurrent generation left staging paths")
	}
}

func TestMCPEnvironmentCannotSelectHarnessAccount(t *testing.T) {
	m, account, root := editManager(t)
	if err := m.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedMCP{Name: "provider-tools", MCPDefinition: MCPDefinition{Command: "node", Args: []string{"tools.js"}, EnvKeys: []string{"OPENAI_API_KEY"}}, EnabledHarnesses: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	generation, err := m.GenerateWithEnv("codex", account, root, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "mcp-secret-canary", true
		}
		if key == "DYNA_BIN" {
			return "/fake/dyna", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	alias := MCPEnvironmentName("OPENAI_API_KEY")
	if alias == "OPENAI_API_KEY" || generation.Env[alias] != "mcp-secret-canary" || generation.Env["OPENAI_API_KEY"] != "" {
		t.Fatal("MCP key escaped its server scope")
	}
	if strings.Contains(strings.Join(generation.Args, " "), "mcp-secret-canary") {
		t.Fatal("secret entered argv")
	}
	raw, err := os.ReadFile(filepath.Join(generation.Directory, "mcp-exec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mcp-secret-canary") {
		t.Fatal("secret entered execution metadata")
	}
	resumed, err := LoadGenerationWithEnv(generation.Directory, func(key string) (string, bool) { return "rotated-mcp-canary", key == "OPENAI_API_KEY" })
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Env[alias] != "rotated-mcp-canary" || resumed.Env["OPENAI_API_KEY"] != "" {
		t.Fatal("resume lost scoped reference")
	}
}

func TestSkillSourceCannotExportCentralNativeAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	source := filepath.Join(store.DataDir(), "accounts", "codex", "fixture")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &ConfigManager{Home: home, Path: filepath.Join(home, "catalog.json")}
	if err := m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "unsafe", Source: source}); err == nil {
		t.Fatal("account data accepted as skill resources")
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	if err := m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "unsafe", Source: alias}); err == nil {
		t.Fatal("account alias accepted as skill resources")
	}
	if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
		t.Fatal("rejected source changed catalog", err)
	}
}

func TestClaudeGenerationLoadsPrivateUserInstructions(t *testing.T) {
	m, account, root := editManager(t)
	g, err := m.Generate("claude", account, root)
	mustEdit(t, err)
	if !strings.Contains(strings.Join(g.Args, "\x00"), "--setting-sources\x00user") {
		t.Fatalf("Claude skips its private CLAUDE.md without the user setting source: %q", g.Args)
	}
}
