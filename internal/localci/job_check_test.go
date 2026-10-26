package localci

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// A job check runs the named workflow job through the pinned act binary, inside
// the same slot, deadline and report handling as every other check: act's
// containers carry the run label and are removed afterwards, and the JUnit the
// job uploaded as an artifact reaches the check's report directory.
func TestJobCheckRunsItsWorkflowJobThroughAct(t *testing.T) {
	dir := t.TempDir()
	// The pinned binary is already installed, so the script skips the download.
	actDir := filepath.Join(dir, "tools", "act-"+actVersion)
	if err := os.MkdirAll(actDir, 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "act.args")
	fakeAct := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argsFile + "\n" +
		"out=\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --artifact-server-path) out=$2; shift 2 ;; *) shift ;; esac; done\n" +
		"mkdir -p \"$out/42/go-test-report\" && printf '<testsuite><testcase name=\"fast\" time=\"0.1\"/></testsuite>' > \"$out/42/go-test-report/go-test.xml\"\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(actDir, "act"), []byte(fakeAct), 0o755); err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(dir, "removed")
	fakeDocker := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  info) exit 0 ;;\n" +
		"  ps) echo 0123456789ab ;;\n" +
		"  rm) printf '%s\\n' \"$@\" >> " + removed + "; exit 0 ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "go-static", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	check := nodeCheck{id: "go-static", name: "go-static", reports: ReportJUnit,
		job:       &JobRef{Workflow: ".github/workflows/ci.yml", Name: "go-shard", Matrix: []string{"shard:7"}},
		resources: checkResources{CPUs: 4, MemoryMiB: 8192}}
	script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "cache.git"), dir, "run-7", strings.Repeat("a", 40), strings.Repeat("b", 40), check, time.Minute, 0, false, "catthehacker/ubuntu:act-22.04")
	cmd := exec.Command("bash", "-c", nodeSlotScript(dir, 1, 0, script))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("job check: %v: %s", err, out.String())
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("act did not run: %v", err)
	}
	for _, want := range []string{"pull_request", "-W", ".github/workflows/ci.yml", "-j", "go-shard", "--matrix", "shard:7", "--rm",
		"-P", "ubuntu-latest=catthehacker/ubuntu:act-22.04", "--cache-server-path", "--artifact-server-path",
		"--container-options", "--cpus 4", "--memory 8192m", "--label dyna.run=run-7"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("act args %q miss %q", args, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "reports", "go-static", "go-test.xml")); err != nil {
		t.Fatalf("the uploaded JUnit did not reach the check's report directory: %v", err)
	}
	if data, err := os.ReadFile(removed); err != nil || !strings.Contains(string(data), "0123456789ab") {
		t.Fatalf("the labelled containers were not removed: %q %v", data, err)
	}
	if !strings.Contains(out.String(), "DYNA_CI_CHECK_EXIT=0") {
		t.Fatalf("job check lost its marker: %s", out.String())
	}
}
