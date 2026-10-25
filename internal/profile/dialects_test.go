package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestHarnessDefaultDialects(t *testing.T) {
	for harness, want := range map[string]string{
		HarnessClaudeCode: "claude-stream-json",
		HarnessOpenCode: "opencode-json", HarnessMuse: "muse-jsonl",
		HarnessPi:   "pi-json",
		HarnessMock: "text",
	} {
		t.Run(harness, func(t *testing.T) {
			p := Profile{Harness: harness}
			if got := p.EffectiveDialect(); got != want {
				t.Fatalf("effective dialect = %q, want %q", got, want)
			}
			if p.Output != nil {
				t.Fatal("resolving a default changed the profile")
			}
			if got := p.EffectiveOutput(); got == nil || got.Dialect != want {
				t.Fatalf("effective output = %#v", got)
			}
		})
	}
}

func TestEveryHarnessHasDefaultDialect(t *testing.T) {
	for _, harness := range Harnesses {
		definition, ok := HarnessDefinitionFor(harness)
		dialect := definition.Dialect
		if !ok || dialect == "" {
			t.Errorf("harness %q has no default dialect", harness)
			continue
		}
		if err := workeroutput.ValidateDeclaration(&workeroutput.Declaration{Dialect: dialect}); err != nil {
			t.Error(err)
		}
	}
}

func TestDialectMustMatchHarness(t *testing.T) {
	for _, harness := range Harnesses {
		definition, _ := HarnessDefinitionFor(harness)
		p := Profile{Name: "test", Harness: harness, Taste: 5, Intelligence: 5, Cost: 5}
		if err := p.Validate(); err != nil {
			t.Fatalf("%s default: %v", harness, err)
		}
		p.Output = &workeroutput.Declaration{Dialect: definition.Dialect}
		if err := p.Validate(); err != nil {
			t.Fatalf("%s native: %v", harness, err)
		}
		p.Output.Dialect = workeroutput.DialectMuseJSONL
		if p.Output.Dialect == definition.Dialect {
			p.Output.Dialect = workeroutput.DialectOpenCodeJSON
		}
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), "emits") {
			t.Fatalf("%s mismatch error = %v", harness, err)
		}
	}
}

func TestLegacyProfileDefaultsDoNotRewriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	raw := []byte(`{"version":2,"profiles":[{"name":"legacy","harness":"claude-code","taste":5,"intelligence":5,"cost":5}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Profiles[0].EffectiveDialect(); got != workeroutput.DialectClaudeStreamJSON {
		t.Fatal(got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("loading defaults rewrote the config")
	}
}

func TestHarnessDefinitionsAreCopies(t *testing.T) {
	definition, ok := HarnessDefinitionFor(HarnessClaudeCode)
	if !ok {
		t.Fatal("missing Claude registry entry")
	}
	definition.VersionArgs[0] = "modified"
	definition.Fixtures[0] = "modified"
	again, _ := HarnessDefinitionFor(HarnessClaudeCode)
	if again.VersionArgs[0] != "--version" || again.Fixtures[0] != "claude-tools.jsonl" {
		t.Fatal("caller mutated registry")
	}
}

func TestBlankProfileDeclarationUsesHarnessDefault(t *testing.T) {
	for _, harness := range Harnesses {
		definition, _ := HarnessDefinitionFor(harness)
		p := Profile{Name: "legacy-empty", Harness: harness, Taste: 5, Intelligence: 5, Cost: 5,
			Output: &workeroutput.Declaration{Dialect: "  "}}
		if err := p.Validate(); err != nil {
			t.Fatalf("%s empty optional dialect: %v", harness, err)
		}
		if got := p.EffectiveOutput().Dialect; got != definition.Dialect {
			t.Fatalf("%s resolved to %q", harness, got)
		}
	}
}
