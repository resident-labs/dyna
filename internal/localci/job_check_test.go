package localci

import (
	"archive/zip"
	"bytes"
	"errors"
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
	// act stores an upload as <artifact>/<artifact>.zip, not loose XML files.
	artifactZip := filepath.Join(dir, "upload.zip")
	archive, err := os.Create(artifactZip)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	// Two packages upload a report with the same file name, so the check must
	// keep both instead of letting one overwrite the other.
	// The flattened names of these two entries would collide, so only an
	// ordinal per entry keeps both.
	for _, pkg := range []string{"package_a", "package/a"} {
		entry, err := zipWriter.Create(pkg + "/go-test.xml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(`<testsuite><testcase name="` + pkg + `" time="0.1"/></testsuite>`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "act.args")
	envFile := filepath.Join(dir, "act.env.seen")
	fakeAct := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argsFile + "\n" +
		"env > " + envFile + "\n" +
		"out=\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --artifact-server-path) out=$2; shift 2 ;; *) shift ;; esac; done\n" +
		"mkdir -p \"$out/42/go-test-report\" && cp " + artifactZip + " \"$out/42/go-test-report/go-test-report.zip\"\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(actDir, "act"), []byte(fakeAct), 0o755); err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(dir, "removed")
	listed := filepath.Join(dir, "listed")
	fakeDocker := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  info) exit 0 ;;\n" +
		"  ps) printf '%s\\n' \"$@\" >> " + listed + "; echo 0123456789ab ;;\n" +
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
	script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "cache.git"), dir, "run-7", strings.Repeat("a", 40), strings.Repeat("b", 40), check, time.Minute, 0, false, "catthehacker/ubuntu:act-22.04", "unix:///run/user/1000/podman/podman.sock")
	cmd := exec.Command("bash", "-c", nodeSlotScript(dir, 1, 0, script))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A host-only variable stands in for a session token or an account key.
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "DYNA_HOST_ONLY_SECRET=do-not-leak")
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
		"--artifact-server-port", "--container-daemon-socket", "-",
		"--container-options", "--cpus 4", "--memory 8192m", "--label dyna.run=run-7", "--label dyna.check=go-static"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("act args %q miss %q", args, want)
		}
	}
	seen, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("act environment not recorded: %v", err)
	}
	if !strings.Contains(string(seen), "DOCKER_HOST=unix:///run/user/1000/podman/podman.sock") {
		t.Fatalf("act did not get the machine's runtime socket: %s", seen)
	}
	if strings.Contains(string(seen), "DYNA_HOST_ONLY_SECRET") {
		t.Fatalf("a host variable reached act: %s", seen)
	}
	if !strings.Contains(string(seen), "PATH=/usr/local/sbin:") {
		t.Fatalf("act did not get the fixed PATH: %s", seen)
	}
	// Both uploaded reports reach the check's report directory, each under its
	// own name, and the reader sees both.
	reportDir := filepath.Join(dir, "reports", "go-static")
	reports, err := filepath.Glob(filepath.Join(reportDir, "*.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %v, want one per uploaded package", reports)
	}
	var found []string
	for _, report := range reports {
		body, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, string(body))
	}
	for _, want := range []string{"package_a", "package/a"} {
		if !strings.Contains(strings.Join(found, "\n"), `name="`+want+`"`) {
			t.Fatalf("report %q lost the tests of %s", found, want)
		}
	}
	data, err := os.ReadFile(removed)
	if err != nil {
		t.Fatalf("the containers were not removed: %v", err)
	}
	if !strings.Contains(string(data), "0123456789ab") {
		t.Fatalf("cleanup did not remove the listed container: %q", data)
	}
	// The cleanup names the run and the check, so it never removes a sibling
	// check's containers.
	filters, err := os.ReadFile(listed)
	if err != nil {
		t.Fatalf("the containers were not listed: %v", err)
	}
	for _, want := range []string{"label=dyna.run=run-7", "label=dyna.check=go-static"} {
		if !strings.Contains(string(filters), want) {
			t.Fatalf("cleanup filter %q miss %q", filters, want)
		}
	}
	if !strings.Contains(out.String(), "DYNA_CI_CHECK_EXIT=0") {
		t.Fatalf("job check lost its marker: %s", out.String())
	}
}

// A workflow job runs containers the workflow chooses, so it needs the same
// host-check grant a host command needs; without the grant the check is
// refused before act starts.
func TestJobCheckNeedsTheHostCheckGrant(t *testing.T) {
	definition, err := ParseDefinition([]byte("version: 1\ntrial: [ci]\nchecks:\n  - id: build\n    job: { workflow: .github/workflows/ci.yml, name: build }\nsuites:\n  - id: ci\n    checks: [build]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := nodeChecks(definition, []Suite{"ci"}, false); !errors.Is(err, ErrHostChecksDisabled) {
		t.Fatalf("error = %v, want %v", err, ErrHostChecksDisabled)
	}
	if _, _, err := nodeChecks(definition, []Suite{"ci"}, true); err != nil {
		t.Fatalf("a granted machine refused the job check: %v", err)
	}
}
