package localci

import (
	"strings"
	"testing"
)

// A check may name one job of a GitHub Actions workflow instead of a command of
// its own, so a repository keeps one CI definition. Everything Dyna runs itself
// is empty then; act runs the job.
func TestJobCheckNamesAWorkflowJobInsteadOfACommand(t *testing.T) {
	definition, err := ParseDefinition([]byte(`version: 1
trial: [ci]
checks:
  - id: go-static
    job: { workflow: .github/workflows/ci.yml, name: go-static }
  - id: shard-4
    name: go-test-shard (4)
    job: { workflow: .github/workflows/ci.yml, name: go-shard }
    cpus: 4
    memory: 8g
suites:
  - id: ci
    files: ['^internal/']
    checks: [go-static, shard-4]
`))
	if err != nil {
		t.Fatalf("a job check was rejected: %v", err)
	}
	suite, ok := definition.bySuite[Suite("ci")]
	if !ok || len(suite.checks) != 2 {
		t.Fatalf("suite ci = %+v", suite)
	}
	for i, want := range []struct {
		name     string
		workflow string
		job      string
	}{
		{"go-static", ".github/workflows/ci.yml", "go-static"},
		{"go-test-shard (4)", ".github/workflows/ci.yml", "go-shard"},
	} {
		check := suite.checks[i]
		if check.Job == nil {
			t.Fatalf("%s has no job reference", check.Check.Name)
		}
		if check.Job.Workflow != want.workflow || check.Job.Name != want.job {
			t.Fatalf("%s job = %+v, want %s/%s", check.Check.Name, *check.Job, want.workflow, want.job)
		}
		if check.Command != "" || check.Image != "" || check.Host || len(check.Caches) != 0 {
			t.Fatalf("%s also carries a command, image, host mode or caches: %+v", check.Check.Name, check)
		}
	}
	if suite.checks[1].MemoryMiB != 8<<10 {
		t.Fatalf("the job check lost its memory request: %d", suite.checks[1].MemoryMiB)
	}
}

// A job check cannot also ask Dyna to run its own command or container, and the
// workflow path may not leave the repository.
func TestJobCheckRejectsContradictions(t *testing.T) {
	for _, tc := range []struct{ name, check, want string }{
		{"command", "    command: go build ./...\n    job: { workflow: .github/workflows/ci.yml, name: build }", "cannot also set command"},
		{"image", "    image: golang:1.26.3\n    job: { workflow: .github/workflows/ci.yml, name: build }", "cannot also set command, image"},
		{"outside", "    job: { workflow: ../ci.yml, name: build }", "must be one file under .github/workflows"},
		{"not a workflow", "    job: { workflow: ci/ci.yml, name: build }", "must be one file under .github/workflows"},
		{"display name", "    job: { workflow: .github/workflows/ci.yml, name: 'go static' }", "must be a job id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDefinition([]byte("version: 1\ntrial: [ci]\nchecks:\n  - id: build\n" + tc.check + "\nsuites:\n  - id: ci\n    checks: [build]\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
