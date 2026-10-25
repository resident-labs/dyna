package localci

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

func TestDynaDefinitionMirrorsBuiltInGraph(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(DefinitionPath)))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ParseDefinition(body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := definition.Suites(), Suites(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defined suites %v, want %v", got, want)
	}
	if got, want := definition.WorkflowSuites(), WorkflowSuites(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defined trial %v, want %v", got, want)
	}
	for _, suite := range Suites() {
		got, gotOK := definition.Checks(suite)
		want, wantOK := Checks(suite)
		if gotOK != wantOK || !reflect.DeepEqual(got, want) {
			t.Errorf("suite %q checks %v, %t; want %v, %t", suite, got, gotOK, want, wantOK)
		}
	}
	for _, files := range [][]string{{"relay/server.ts"}, {"internal/pr/watch/service.go"}, {"web/src/main.ts"}, {"internal/computers/service.go"}, {"docs/README.md"}} {
		if got, want := definition.SuitesForFiles(files), SuitesForFiles(files); !reflect.DeepEqual(got, want) {
			t.Errorf("files %v selected %v, want %v", files, got, want)
		}
	}
	for id, command := range nodeCommands {
		if IsShard(id) || id == SuiteComputerSmoke {
			continue
		}
		checks, ok := definition.ExecutableChecks(id)
		if !ok || len(checks) != 1 {
			t.Errorf("defined command for %q is missing", id)
			continue
		}
		if checks[0].Image != command.image || checks[0].Command != command.inner {
			t.Errorf("check %q = image %q, command %q; want image %q, command %q", id, checks[0].Image, checks[0].Command, command.image, command.inner)
		}
	}
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
