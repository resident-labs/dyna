package localci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/repoid"
)

func TestLoadDefinitionFromRepository(t *testing.T) {
	repo := t.TempDir()
	definitionGit(t, repo, "init", "--quiet")
	writeRepositoryDefinition(t, repo, `version: 1
trial: [test]
caches:
  - name: pip
    target: /cache/pip
    env: PIP_CACHE_DIR
checks:
  - id: pytest
    image: python:3.13-slim
    command: python -m pip install -e . && python -m pytest --junitxml=/reports/pytest.xml
    caches: [pip]
    reports: junit
suites:
  - id: test
    files: ['^src/', '^tests/', '^pyproject\.toml$']
    checks: [pytest]
`)
	definitionGit(t, repo, "add", DefinitionPath)
	definitionGit(t, repo, "-c", "user.name=Dyna Test", "-c", "user.email=dyna@example.invalid", "commit", "--quiet", "-m", "add CI definition")

	definition, err := LoadDefinition(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := definition.SuitesForFiles([]string{"src/example_test.py"}), []Suite{"test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected suites %v, want %v", got, want)
	}
	if got := definition.SuitesForFiles([]string{"docs/README.md"}); len(got) != 0 {
		t.Fatalf("documentation selected %v", got)
	}
	if got, want := definition.WorkflowSuites(), []Suite{"test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trial suites %v, want %v", got, want)
	}
	checks, ok := definition.ExecutableChecks("test")
	if !ok || len(checks) != 1 {
		t.Fatalf("executable checks = %v, %t", checks, ok)
	}
	check := checks[0]
	if check.Image != "python:3.13-slim" || !strings.Contains(check.Command, "pytest") {
		t.Fatalf("check used image %q and command %q", check.Image, check.Command)
	}
	if got, want := check.Caches, []CacheDefinition{{Name: "pip", Target: "/cache/pip", Env: "PIP_CACHE_DIR"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("check caches %#v, want %#v", got, want)
	}
	if check.Reports != ReportJUnit {
		t.Fatalf("report mode %q, want %q", check.Reports, ReportJUnit)
	}
	result := Result{Node: "worker", Status: OutcomePassed, Checks: []SuiteResult{{CheckName: "pytest", Status: OutcomePassed}}}
	if !definition.CoversSuites(result, []Suite{"test"}) {
		t.Fatal("a result with the repository's declared check was incomplete")
	}
}

func testDynaDefinition(t *testing.T) *Definition {
	t.Helper()
	// A fixed definition, not this repository's: the tests model checks with
	// commands of their own, and the repository's own file is validated by
	// TestThisRepositoryDefinitionParses.
	body, err := os.ReadFile(filepath.Join("testdata", "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ParseDefinition(body)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestLoadDefinitionAbsentAndStrict(t *testing.T) {
	repo := t.TempDir()
	definitionGit(t, repo, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("no local CI\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionGit(t, repo, "add", "README.md")
	definitionGit(t, repo, "-c", "user.name=Dyna Test", "-c", "user.email=dyna@example.invalid", "commit", "--quiet", "-m", "initial")
	definition, err := LoadDefinition(t.Context(), repo, "HEAD")
	if definition != nil || !errors.Is(err, ErrDefinitionNotFound) {
		t.Fatalf("missing definition = %v, %v; want ErrDefinitionNotFound", definition, err)
	}

	writeRepositoryDefinition(t, repo, "version: 1\nunknown: true\n")
	definitionGit(t, repo, "add", DefinitionPath)
	definitionGit(t, repo, "-c", "user.name=Dyna Test", "-c", "user.email=dyna@example.invalid", "commit", "--quiet", "-m", "invalid CI definition")
	if _, err := LoadDefinition(t.Context(), repo, "HEAD"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown key error = %v", err)
	}
}

func TestDefinitionRejectsAdditionalYAMLDocuments(t *testing.T) {
	body := []byte(`version: 1
trial: [test]
checks:
  - id: pytest
    image: python:3.13-slim
    command: python -m pytest
suites:
  - id: test
    checks: [pytest]
---
version: 1
trial: [other]
checks:
  - id: ignored
    image: python:3.13-slim
    command: python -m unittest
suites:
  - id: other
    checks: [ignored]
`)
	if _, err := ParseDefinition(body); err == nil || !strings.Contains(err.Error(), "one YAML document") {
		t.Fatalf("additional document error = %v", err)
	}
}

func TestDefinitionRejectsRunnerDirectoryCacheNames(t *testing.T) {
	for _, name := range []string{"runs", "slots"} {
		t.Run(name, func(t *testing.T) {
			body := fmt.Sprintf(`version: 1
trial: [test]
caches:
  - name: %s
    target: /cache/data
checks:
  - id: pytest
    image: python:3.13-slim
    command: python -m pytest
    caches: [%s]
suites:
  - id: test
    checks: [pytest]
`, name, name)
			if _, err := ParseDefinition([]byte(body)); err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("reserved cache name %q was accepted: %v", name, err)
			}
		})
	}
}

// A check may ask for more CPUs and memory than the default, but the node bounds
// the request, says so, and runs the command with the granted size.
func TestDefinitionBoundsCheckResourcesByMachine(t *testing.T) {
	definition, err := ParseDefinition([]byte(`version: 1
trial: [test]
checks:
  - id: cargo-test
    image: rust:1
    command: cargo nextest run --workspace --no-run
    cpus: 32
    memory: 64g
  - id: plain
    image: rust:1
    command: cargo test
suites:
  - id: test
    checks: [cargo-test, plain]
`))
	if err != nil {
		t.Fatal(err)
	}
	checks, _, err := nodeChecks(definition, []Suite{"test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].cpus != 32 || checks[0].memoryMiB != 64*1024 {
		t.Fatalf("parsed request = %d cpus and %d MiB", checks[0].cpus, checks[0].memoryMiB)
	}
	// A machine of 8 slots, 20 CPUs and 32 GiB grants the slot pool's 16 CPUs
	// and its own memory, not the request.
	node := Node{Name: "arch", Slots: 8, Enabled: true, CPUs: 20, MemoryBytes: 32 << 30}
	granted := clampCheckResources(checks[0], node)
	if granted.CPUs != 16 || granted.MemoryMiB != 32*1024 {
		t.Fatalf("granted %+v, want 16 cpus and 32 GiB", granted)
	}
	if want := "Dyna clamped cpus from 32 to 16 and memory from 64 GiB to 32 GiB"; granted.Note != want {
		t.Fatalf("note %q, want %q", granted.Note, want)
	}
	// The granted size is what the container command runs with, and the note
	// reaches the check's own log.
	checks[0].resources = granted
	script := nodeCheckScript(node, "/base", "/cache", "/run", "run", strings.Repeat("a", 40), strings.Repeat("b", 40), checks[0], time.Minute, 0, false, DefaultActImage, machines.DefaultContainerSocket, "")
	for _, want := range []string{"--cpus=16", "--memory=32g", "--memory-swap=32g", granted.Note} {
		if !strings.Contains(script, want) {
			t.Errorf("container check lacks %q", want)
		}
	}
	// A probe of the machine that fits the request changes nothing.
	roomy := clampCheckResources(checks[0], Node{Name: "hermeslab", Slots: 16, Enabled: true, CPUs: 32, MemoryBytes: 64 << 30})
	if roomy != (checkResources{CPUs: 32, MemoryMiB: 64 * 1024}) {
		t.Fatalf("roomy machine granted %+v, want the request unchanged", roomy)
	}
	// A check that asks for nothing keeps today's defaults, and a host check
	// keeps its larger default memory.
	plain := clampCheckResources(checks[1], node)
	if plain != (checkResources{CPUs: defaultCheckCPUs, MemoryMiB: defaultContainerMemoryMiB}) {
		t.Fatalf("default check granted %+v", plain)
	}
	if host := clampCheckResources(nodeCheck{host: true}, node); host.MemoryMiB != defaultHostMemoryMiB {
		t.Fatalf("host check granted %+v", host)
	}
	// A host check receives its granted CPU share and memory through its systemd
	// scope.
	var scope []string
	runner := Runner{Node: &node}
	runner.Command = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		scope = append([]string{name}, args...)
		return nil
	}
	if err := runner.nodeCommandScoped(t.Context(), io.Discard, io.Discard, "dyna-ci-x", checkResources{CPUs: 16, MemoryMiB: 32 * 1024}.scope(true), "true"); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(scope, " "); !strings.Contains(joined, "CPUQuota=1600%") || !strings.Contains(joined, "MemoryMax=32G") {
		t.Fatalf("host scope = %s", joined)
	}
	// A container keeps the GiB above its own limit that it always had, so the
	// docker client fits in the scope.
	if got := granted.scope(false).MemoryMiB; got != 33*1024 {
		t.Fatalf("container scope memory = %d MiB, want 33 GiB", got)
	}
	// A reader cannot mean these, so the parse says so.
	for _, body := range []string{
		"    cpus: -1\n",
		"    memory: 4\n",
		"    memory: 4gb\n",
		"    memory: 0g\n",
	} {
		if _, err := ParseDefinition([]byte(`version: 1
trial: [test]
checks:
  - id: cargo-test
    image: rust:1
    command: cargo test
` + body + "suites:\n  - id: test\n    checks: [cargo-test]\n")); err == nil {
			t.Fatalf("%q was accepted", strings.TrimSpace(body))
		}
	}
}

func writeRepositoryDefinition(t *testing.T, repo, body string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(DefinitionPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func definitionGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := repoid.Git(t.Context(), repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
