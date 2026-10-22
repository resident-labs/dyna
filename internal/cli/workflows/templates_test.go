package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workflowtemplate"
)

func execTemplates(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	cmd := NewTemplatesCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	if stdin != "" {
		cmd.SetIn(strings.NewReader(stdin))
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), err
}

func TestTemplatesListJSONContractAndOrdering(t *testing.T) {
	serveTemplateSeeds(t)
	registry := filepath.Join(t.TempDir(), "registry")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "zeta.js"), []byte("export const meta = { description: 'user zeta' }\nreturn 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "alpha.js"), []byte("return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := execTemplates(t, "", "list", "--json", "--templates-dir", registry)
	if err != nil {
		t.Fatal(err)
	}
	var items []workflowtemplate.Info
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("list json: %v\n%s", err, out)
	}
	if len(items) != 2 || items[0].Name != "alpha" || items[0].Source != workflowtemplate.SourceUser || items[1].Name != "zeta" || items[1].Source != workflowtemplate.SourceUser {
		t.Fatalf("list = %#v", items)
	}
}

func TestTemplatesListJSONEmptyIsArray(t *testing.T) {
	serveTemplateSeeds(t)
	out, err := execTemplates(t, "", "list", "--json", "--templates-dir", filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list json = %q, want []", out)
	}
}

func TestTemplatesSaveStdinAndShowJSON(t *testing.T) {
	serveTemplateSeeds(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	src := "export const meta = { name: 'echo', description: 'returns args' }\nreturn args\n"
	out, err := execTemplates(t, src, "save", "echo", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved echo") {
		t.Fatalf("save output = %q", out)
	}

	shown, err := execTemplates(t, "", "show", "echo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result workflowtemplate.ShowResult
	if err := json.Unmarshal([]byte(shown), &result); err != nil {
		t.Fatal(err)
	}
	if result.Name != "echo" || result.Source != workflowtemplate.SourceUser || result.Content != src || result.Description != "returns args" {
		t.Fatalf("show = %#v", result)
	}

	if _, err := execTemplates(t, src, "save", "../echo", "--stdin"); err == nil {
		t.Fatal("save accepted an unsafe name")
	}
	if _, err := execTemplates(t, src, "save", "echo"); err == nil {
		t.Fatal("save without --stdin succeeded")
	}
	if _, err := execTemplates(t, src, "save", "echo", "--stdin"); err == nil {
		t.Fatal("second save without --overwrite succeeded")
	}
	updated := "export const meta = { name: 'echo', description: 'updated' }\nreturn args\n"
	out, err = execTemplates(t, updated, "save", "echo", "--stdin", "--overwrite", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var saved workflowtemplate.SaveResult
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Overwritten || saved.Name != "echo" {
		t.Fatalf("overwrite save = %#v", saved)
	}
	shown, err = execTemplates(t, "", "show", "echo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(shown), &result); err != nil {
		t.Fatal(err)
	}
	if result.Content != updated || result.Description != "updated" {
		t.Fatalf("show after overwrite = %#v", result)
	}
}

func TestTemplatesCopySafetyAndJSON(t *testing.T) {
	serveTemplateSeeds(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	src := "export const meta = { name: 'echo' }\nreturn 1\n"
	if _, err := execTemplates(t, src, "save", "echo", "--stdin"); err != nil {
		t.Fatal(err)
	}

	if _, err := execTemplates(t, "", "copy", "echo"); err == nil {
		t.Fatal("copy without --to succeeded")
	}
	if _, err := execTemplates(t, "", "copy", "echo", "--to", filepath.Join(t.TempDir(), "review.txt")); err == nil {
		t.Fatal("copy accepted a non-js destination")
	}

	dest := filepath.Join(t.TempDir(), "scratch", "review.js")
	out, err := execTemplates(t, "", "copy", "echo", "--to", dest, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result workflowtemplate.CopyResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Name != "echo" || result.Overwritten || result.Destination != dest {
		t.Fatalf("copy = %#v", result)
	}
	got, err := os.ReadFile(result.Destination)
	if err != nil || string(got) != src {
		t.Fatalf("copied bytes = %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dest), ".catalog.lock")); !os.IsNotExist(err) {
		t.Fatalf("destination lockfile should not exist: %v", err)
	}

	if err := os.WriteFile(dest, []byte("return 'changed'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := execTemplates(t, "", "copy", "echo", "--to", dest, "--json"); err == nil {
		t.Fatal("copy overwrote without --overwrite")
	}
	out, err = execTemplates(t, "", "copy", "echo", "--to", dest, "--overwrite", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Overwritten {
		t.Fatalf("overwrite copy = %#v", result)
	}
}

func TestTemplatesIgnoresCwdExamplesAndRemovedFlags(t *testing.T) {
	serveTemplateSeeds(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "examples"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "examples", "echo.js"), []byte("return {from:'examples'}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	if _, err := execTemplates(t, "", "list", "--dir", work); err == nil {
		t.Fatal("templates still accepts --dir")
	}
	if _, err := execTemplates(t, "", "show", "echo", "--source", "examples"); err == nil {
		t.Fatal("templates still accepts --source")
	}
	if _, err := execTemplates(t, "", "copy", "echo", "--source", "examples", "--to", filepath.Join(t.TempDir(), "out.js")); err == nil {
		t.Fatal("copy still accepts --source")
	}

	registry := filepath.Join(cfg, "dyna", "templates")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := execTemplates(t, "", "list", "--json", "--templates-dir", registry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("cwd examples leaked into list: %s", out)
	}
}

func TestTemplatesSeedFreshAndDefaultList(t *testing.T) {
	serveTemplateSeeds(t)
	registry := filepath.Join(t.TempDir(), "templates")
	out, err := execTemplates(t, "", "seed", "--json", "--templates-dir", registry)
	if err != nil {
		t.Fatal(err)
	}
	var result workflowtemplate.SeedResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Written) < 3 {
		t.Fatalf("seed written = %#v", result.Written)
	}

	listed, err := execTemplates(t, "", "list", "--json", "--templates-dir", registry)
	if err != nil {
		t.Fatal(err)
	}
	var items []workflowtemplate.Info
	if err := json.Unmarshal([]byte(listed), &items); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, item := range items {
		names[item.Name] = true
		if item.Source != workflowtemplate.SourceUser {
			t.Fatalf("list source = %#v", item)
		}
	}
	for _, name := range []string{"adversarial-review", "judge-panel", "research-design"} {
		if !names[name] {
			t.Fatalf("list missing %s: %#v", name, items)
		}
	}

	again, err := execTemplates(t, "", "seed", "--json", "--templates-dir", registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(again), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Unchanged) < 3 || len(result.Written)+len(result.Updated) != 0 {
		t.Fatalf("idempotent CLI seed = %#v", result)
	}
}

func TestTemplatesDefaultListSeedsShipped(t *testing.T) {
	serveTemplateSeeds(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, err := execTemplates(t, "", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var items []workflowtemplate.Info
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("default list did not seed shipped starters: %#v", items)
	}
}

func TestCanonicalExampleStartersExist(t *testing.T) {
	root := filepath.Join("..", "..", "..", "examples")
	for _, name := range []string{"adversarial-review.js", "judge-panel.js", "research-design.js"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "export const meta") {
			t.Fatalf("%s should expose template metadata", name)
		}
	}
}

func serveTemplateSeeds(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dataDir := t.TempDir()
	store := workflowtemplate.Store{DataDir: dataDir}
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "TemplateSeed", Access: websvc.Write, Scope: websvc.ScopeAdmin,
		Method: func(ctx context.Context, registry string) (workflowtemplate.SeedResult, error) {
			return store.SeedShipped(ctx, workflowtemplate.New(registry))
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, dataDir, calls)
}

func TestTemplateSeedWithoutEngineFailsWithoutLocalWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saved := templateEngineClient
	t.Cleanup(func() { templateEngineClient = saved })
	templateEngineClient = func(...engineclient.Option) *engineclient.Client {
		return engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	if _, err := execTemplates(t, "", "seed"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("seed without engine: %v", err)
	}
	if _, err := os.Stat(workflowtemplate.DefaultRegistryDir()); !os.IsNotExist(err) {
		t.Fatalf("CLI wrote the registry without the engine: %v", err)
	}
}

func TestDefaultTemplateListWarnsAndContinuesWithoutEngine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry := workflowtemplate.DefaultRegistryDir()
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "saved.js"), []byte("return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd := NewTemplatesCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("automatic seed blocked an existing template read: %v", err)
	}
	var items []workflowtemplate.Info
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "saved" || !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("list output = %q; warning = %q", stdout.String(), stderr.String())
	}
}

func TestAutomaticTemplateSeedWarnsWhenRegistryIsBusy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "TemplateSeed", Access: websvc.Write, Scope: websvc.ScopeAdmin,
		Method: func(context.Context, string) (workflowtemplate.SeedResult, error) {
			return workflowtemplate.SeedResult{}, workflowtemplate.ErrBusy
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, t.TempDir(), calls)
	var stdout, stderr bytes.Buffer
	cmd := NewTemplatesCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("automatic seed blocked list on a busy registry: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "[]" || !strings.Contains(stderr.String(), "busy") {
		t.Fatalf("list output = %q; warning = %q", stdout.String(), stderr.String())
	}
	if _, err := execTemplates(t, "", "seed"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("explicit seed on a busy registry: %v", err)
	}
}
