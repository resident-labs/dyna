package localci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedGoTestStepRunsWithoutRaceAndPreservesPipelineFailure(t *testing.T) {
	for _, event := range []string{"push", "pull_request"} {
		t.Run(event, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			goScript := `#!/bin/sh
printf '%s\n' "$*" >> "$CALL_LOG"
printf '{"Action":"pass"}\n'
exit "${FAIL_GO:-0}"
`
			checker := `#!/bin/sh
printf '%s\n' "$*" >> "$CALL_LOG"
cat >/dev/null
`
			for name, script := range map[string]string{"go": goScript, "dyna-ci": checker} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			run := func(fail bool) error {
				cmd := exec.Command("bash", "../../scripts/ci.sh", "go", "test")
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RUNNER_TEMP="+dir, "CI_EVENT="+event, "CALL_LOG="+log, "CI_SHARD=")
				if fail {
					cmd.Env = append(cmd.Env, "FAIL_GO=1")
				}
				out, err := cmd.CombinedOutput()
				if len(out) > 0 {
					t.Log(string(out))
				}
				return err
			}
			if err := run(false); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "-race") {
				t.Fatalf("event=%s ran with the race detector: %s", event, data)
			}
			isolate := "tests --isolate-over 1s --junit-output " + dir + "/go-test.xml"
			if !strings.Contains(string(data), isolate) {
				t.Fatalf("checker args: %s", data)
			}
			if err := run(true); err == nil {
				t.Fatal("pipeline hid go test failure")
			}
		})
	}
}

func TestSharedNodeInstallUsesCachedDataAndPreservesFailure(t *testing.T) {
	for _, suite := range []string{"frontend", "relay"} {
		t.Run(suite, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			npm := `#!/bin/sh
printf '%s\n' "$*" >> "$CALL_LOG"
if [ "$1" = ci ]; then
  if [ "${2:-}" != --prefer-offline ] || [ "${3:-}" != --no-audit ]; then
    echo "warm install would perform unnecessary registry work" >&2
    exit 74
  fi
  exit "${INSTALL_EXIT:-0}"
fi
`
			for name, body := range map[string]string{"npm": npm, "git": "#!/bin/sh\nexit 0\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			run := func(installExit string) error {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", "../../scripts/ci.sh", suite)
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RUNNER_TEMP="+dir,
					"CALL_LOG="+log, "INSTALL_EXIT="+installExit, "JUNIT_REPORT_PATH="+filepath.Join(dir, "tests.xml"))
				out, err := cmd.CombinedOutput()
				if len(out) > 0 {
					t.Logf("%s", out)
				}
				return err
			}
			if err := run("0"); err != nil {
				t.Fatalf("cached install: %v", err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) < 3 || lines[0] != "ci --prefer-offline --no-audit" {
				t.Fatalf("install or following checks did not run: %s", data)
			}
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			var exit *exec.ExitError
			if err := run("73"); !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("install failure status changed: %v", err)
			}
			data, err = os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "ci --prefer-offline --no-audit\n" {
				t.Fatalf("checks ran after a failed install: %s", data)
			}
		})
	}
}

func TestGoContainerPreparesIdentityBeforeDroppingPrivileges(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "identity")
	stubs := map[string]string{
		"docker": `#!/bin/sh
case "$1" in
  info) exit 0 ;;
  run)
    for arg; do last=$arg; done
    printf '%s' "$last" > "$CONTAINER_INNER"
    CI_UID=1001 CI_GID=1001 bash "$CONTAINER_INNER"
    ;;
  *) exit 0 ;;
esac
`,
		"git":      "#!/bin/sh\nexit 0\n",
		"apt-get":  "#!/bin/sh\nexit 0\n",
		"getent":   "#!/bin/sh\nexit 2\n",
		"groupadd": "#!/bin/sh\nexit 0\n",
		"useradd":  "#!/bin/sh\ntouch \"$IDENTITY_MARKER\"\n",
		"setpriv":  "#!/bin/sh\ntest -f \"$IDENTITY_MARKER\"\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	check := nodeCheck{id: SuiteGoStatic, name: "go-static", inner: "bash scripts/ci.sh go vet", image: "golang:1.26.3", hostUser: true}
	script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "repo.git"), filepath.Join(dir, "run"), "run", strings.Repeat("a", 40), check, time.Minute, 0, false)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "IDENTITY_MARKER="+marker, "CONTAINER_INNER="+filepath.Join(dir, "inner"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("container failed: %v: %s", err, out)
	}
}

// The gate is trusted test infrastructure, separate from each target checkout.
func writeTestHostGate(t *testing.T, dir string) string {
	t.Helper()
	trusted, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-runner-slot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dir, "host-gate")
	if err := os.WriteFile(gate, trusted, 0700); err != nil {
		t.Fatal(err)
	}
	return gate
}

func TestSlotReleaseIsIdempotentUnderRunnerErrexit(t *testing.T) {
	for _, state := range []string{"already-ended", "record-removed", "other-workspace"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			gate := writeTestHostGate(t, dir)
			release := filepath.Join(dir, "ci-runner-slot-release.sh")
			if err := os.Rename(gate, release); err != nil {
				t.Fatal(err)
			}
			slots := filepath.Join(dir, "slots")
			if err := os.Mkdir(slots, 0700); err != nil {
				t.Fatal(err)
			}
			owner := "/work/mine"
			if state == "other-workspace" {
				owner = "/work/other"
			}
			if err := os.WriteFile(filepath.Join(slots, "slot.1.owner"), []byte(owner), 0600); err != nil {
				t.Fatal(err)
			}
			if state != "record-removed" {
				// Linux PID allocation cannot reach INT_MAX. This never targets
				// a live fixture, unrelated process, or process group.
				if err := os.WriteFile(filepath.Join(slots, "slot.1.keeper"), []byte("2147483647"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			reservation := filepath.Join(slots, "slot.1.container")
			if err := os.WriteFile(reservation, []byte("reserved-container"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-e", "-u", "-o", "pipefail", release)
			cmd.Env = append(os.Environ(), "GITHUB_JOB=test", "GITHUB_WORKSPACE=/work/mine", "CI_SLOT_DIR="+slots)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("completion hook failed for %s: %v: %s", state, err, out)
			}
			if data, err := os.ReadFile(reservation); err != nil || string(data) != "reserved-container" {
				t.Fatalf("release removed or changed reservation: %q: %v", data, err)
			}
		})
	}
}
