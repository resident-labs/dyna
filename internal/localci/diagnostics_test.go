package localci

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type diagnosticRecord struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

func readDiagnostics(t *testing.T, path string) ([]diagnosticRecord, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 64<<10 {
		t.Fatalf("diagnostics exceed 64 KiB: %d", len(data))
	}
	var records []diagnosticRecord
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var record diagnosticRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("invalid diagnostic JSON: %v: %q", err, scanner.Text())
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records, data
}

func TestFailureDiagnosticsNeedNoExternalCommand(t *testing.T) {
	dir := t.TempDir()
	proc, cgroup := filepath.Join(dir, "proc"), filepath.Join(dir, "cgroup")
	files := map[string]string{
		"proc/self/limits":              "Max processes             64                   64                   processes\n",
		"proc/self/cgroup":              "0::/tenant/job\n",
		"proc/self/environ":             "GOMAXPROCS=2\x00ACCESS_TOKEN=must-not-leak\x00",
		"proc/123/status":               "Name:\tchild\nState:\tS\nPid:\t123\nPPid:\t7\nUid:\t1001\nThreads:\t4\n",
		"proc/123/environ":              "GOFLAGS=-p=2\x00CC=gcc\x00PASSWORD=must-not-leak\x00",
		"cgroup/tenant/job/pids.max":    "512\n",
		"cgroup/tenant/job/pids.events": "max 9\n",
		"cgroup/tenant/pids.events":     "max 12\n",
		"cgroup/pids.events":            "max 15\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper, err := filepath.Abs("../../scripts/ci-diagnostics.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-euc", `
source "$HELPER"
ci_diag_init
PATH=/no-external-programs
ci_diag_snapshot failure "$PROC_FIXTURE" "$CGROUP_FIXTURE"
`)
	cmd.Env = append(os.Environ(), "HELPER="+helper, "RUNNER_TEMP="+dir, "PROC_FIXTURE="+proc, "CGROUP_FIXTURE="+cgroup)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("builtin snapshot failed: %v: %s", err, out)
	}
	_, data := readDiagnostics(t, filepath.Join(dir, "ci-diagnostics.jsonl"))
	for _, want := range []string{"max 9", "max 12", "max 15", "Max processes", "GOMAXPROCS", "-p=2", "sampled_threads=4"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
	if strings.Contains(string(data), "must-not-leak") || strings.Contains(string(data), "ACCESS_TOKEN") {
		t.Fatalf("diagnostics exposed an unlisted setting: %s", data)
	}
}

func TestFailureDiagnosticsQuietlySkipUnreadableProcessFiles(t *testing.T) {
	// A Unix socket passes Bash's readability check but cannot be opened as a
	// file. This forces the check/open failure without racing a child exit.
	socketDir, err := os.MkdirTemp("", "ci-diag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	dir := t.TempDir()
	proc, cgroup := filepath.Join(dir, "proc"), filepath.Join(dir, "cgroup")
	for _, name := range []string{"self/limits", "self/cgroup", "self/environ", "123/status", "123/environ"} {
		path := filepath.Join(proc, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(socket, path); err != nil {
			t.Fatal(err)
		}
	}
	helper, err := filepath.Abs("../../scripts/ci-diagnostics.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-euc", `
source "$HELPER"
ci_diag_init
PATH=/no-external-programs
[[ -r "$PROC_FIXTURE/self/environ" && -r "$PROC_FIXTURE/123/status" ]]
ci_diag_snapshot failure "$PROC_FIXTURE" "$CGROUP_FIXTURE"
_ci_diag_phase=fixture/process-exit
trap 'ci_diag_exit "$?"' EXIT
printf 'primary failure\n' >&2
exit 73
`)
	cmd.Env = append(os.Environ(), "HELPER="+helper, "RUNNER_TEMP="+dir, "PROC_FIXTURE="+proc, "CGROUP_FIXTURE="+cgroup)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("primary failure status changed: %v: %s", err, out)
	}
	if string(out) != "primary failure\n" {
		t.Fatalf("diagnostic open failure obscured the primary error: %q", out)
	}
	records, _ := readDiagnostics(t, filepath.Join(dir, "ci-diagnostics.jsonl"))
	for _, record := range records {
		if record.Kind == "failure" && record.Key == "fixture/process-exit" && record.Value == "exit=73" {
			return
		}
	}
	t.Fatal("primary failure is missing from diagnostics")
}

func TestDiagnosticsRemainBoundedAcrossNestedFailures(t *testing.T) {
	dir := t.TempDir()
	helper, err := filepath.Abs("../../scripts/ci-diagnostics.sh")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := json.Marshal(diagnosticRecord{Kind: "fixture", Key: "earlier-phase", Value: strings.Repeat("x", 60000)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci-diagnostics.jsonl"), append(seed, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-euc", `
source "$HELPER"
ci_diag_init
printf -v payload '%04096d' 0
ci_diag_record fixture first-failure "$payload"
ci_diag_init
ci_diag_record fixture second-failure "$payload"
`)
	cmd.Env = append(os.Environ(), "HELPER="+helper, "RUNNER_TEMP="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bounded diagnostics: %v: %s", err, out)
	}
	records, _ := readDiagnostics(t, filepath.Join(dir, "ci-diagnostics.jsonl"))
	if len(records) == 0 || records[len(records)-1].Kind != "truncated" {
		t.Fatalf("missing truncation marker: %+v", records)
	}
}

func TestSharedCIFailurePreservesStatusAndToolchainEvidence(t *testing.T) {
	dir := t.TempDir()
	goStub := `#!/bin/bash
case "$1" in
  env)
    if [[ $2 == CC ]]; then printf 'fixture-cc\n'; else printf '{"GOVERSION":"fixture-go","CC":"fixture-cc"}\n'; fi
    ;;
  mod) printf 'fork: Resource temporarily unavailable\n' >&2; exit 73 ;;
  *) exit 0 ;;
esac
`
	for name, script := range map[string]string{
		"go":         goStub,
		"fixture-cc": "#!/bin/bash\nprintf 'fixture compiler 1.0\\n'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "../../scripts/ci.sh", "go", "modules")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RUNNER_TEMP="+dir)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("failure status changed: %v: %s", err, out)
	}
	records, _ := readDiagnostics(t, filepath.Join(dir, "ci-diagnostics.jsonl"))
	seen := map[string]bool{}
	for _, record := range records {
		seen[record.Kind+":"+record.Key] = true
	}
	for _, key := range []string{"toolchain:go-environment", "toolchain:cc-version", "snapshot:entry", "snapshot:failure", "failure:go/modules"} {
		if !seen[key] {
			t.Errorf("missing %s: %+v", key, records)
		}
	}
}

func TestReadArchiveRejectsOversizedDiagnostics(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "go/ci-diagnostics.jsonl", Mode: 0600, Size: (64 << 10) + 1}); err != nil {
		t.Fatal(err)
	}
	// The diagnostic bound must reject the header before reading its body.
	err := readArchive(&archive, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversized diagnostics: %v", err)
	}
}
