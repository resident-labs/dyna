package podman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/contract"
)

func testSpec() box.RunnerSpec {
	size, _ := box.Size(contract.BoxSizeLarge)
	return box.RunnerSpec{Name: "dyna-ci-pve-1", Image: "localhost/dyna-ci-runner:abc", Config: "jit-secret", Repo: "owner/repo", ID: 42, Slot: 3, Size: size, Cache: "/data/cache", BuildCache: "/data/sock/build-cache"}
}

// frameRunner answers every SSH framed command with success and keeps what it saw.
type frameRunner struct {
	args  [][]string
	input [][]byte
	reply []byte
}

func (r *frameRunner) Run(_ context.Context, in []byte, args ...string) ([]byte, error) {
	r.args = append(r.args, args)
	r.input = append(r.input, in)
	if !slices.Contains(args, "bash") && !strings.Contains(args[len(args)-1], "dyna-frame") {
		return nil, nil
	}
	return append(slices.Clone(r.reply), []byte(fmt.Sprintf("DYNA-END %020d %03d\n", len(r.reply), 0))...), nil
}

func newFrameHost(t *testing.T) (*Host, *frameRunner) {
	t.Helper()
	runner := &frameRunner{}
	dir, err := os.MkdirTemp("", "dyna-r-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h, err := New("user@pve", dir, "pve", runner, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return h, runner
}

func TestRunnerArgsKeepTheContainerSealed(t *testing.T) {
	args := runnerArgs(testSpec())
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--rm", "--timeout=22200", "--network=none", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user 1001:1001",
		"--cpus=8", "--memory=17179869184", "--memory-swap=17179869184", "--pids-limit=8192",
		"--tmpfs /home/runner:rw,exec,U,mode=0755,size=12884901888", "--tmpfs /tmp:rw,exec,mode=1777,size=12884901888",
		"--cgroup-parent=dyna-ci.slice", "--env ACTIONS_RUNNER_INPUT_JITCONFIG", "--env DYNA_CI_REPO=owner/repo",
		"--label dyna.ci.slot=3", "--mount type=volume,source=dyna-ci-egress,destination=/run/dyna-egress,subpath=3,ro",
		"--label dyna.ci.size=large", "--mount type=bind,source=/data/cache,destination=/cache,ro",
		"--mount type=bind,source=/data/cache/ci/3,destination=/build", "--env DYNA_CI_CACHE=/cache", "--env DYNA_CI_BUILD=/build",
		"--mount type=bind,source=/data/sock/build-cache,destination=/run/dyna-build-cache,ro",
		"--mount type=bind,source=/data/cache/ci/3/cargo,destination=/box/cache/cargo",
		"--mount type=bind,source=/data/cache/ci/3/target,destination=/box/work/owner/repo/target",
		"--env CARGO_HOME=/box/cache/cargo", "--env CARGO_TARGET_DIR=/box/work/owner/repo/target",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args miss %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "jit-secret") {
		t.Fatal("the JIT config is on the command line")
	}
	for i, arg := range args {
		if arg == "--volume" || arg == "-v" {
			t.Errorf("a whole volume is mounted, so a job could reach another's socket: %s", args[i+1])
		}
		for _, banned := range []string{"--privileged", "--network=host", "podman.sock", "docker.sock", "--cap-add"} {
			if strings.Contains(arg, banned) {
				t.Errorf("banned argument %s", arg)
			}
		}
	}
}

func TestStartRunnerSendsTheConfigOnStandardInputOnly(t *testing.T) {
	h, runner := newFrameHost(t)
	if err := h.StartRunner(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	last := len(runner.args) - 1
	if strings.Contains(strings.Join(runner.args[last], " "), "jit-secret") {
		t.Fatal("the JIT config is in the SSH arguments")
	}
	if !bytes.Contains(runner.input[last], []byte("jit-secret\n")) {
		t.Fatal("the JIT config did not reach standard input")
	}
}

func TestStartRunnerRefusesUnsafeInput(t *testing.T) {
	h, _ := newFrameHost(t)
	for _, change := range []func(*box.RunnerSpec){
		func(s *box.RunnerSpec) { s.Name = "-rf" },
		func(s *box.RunnerSpec) { s.Config = "two\nlines" },
		func(s *box.RunnerSpec) { s.Config = "" },
		func(s *box.RunnerSpec) { s.Image = "" },
		func(s *box.RunnerSpec) { s.Slot = -1 },
		func(s *box.RunnerSpec) { s.Slot = box.RunnerSlots },
		func(s *box.RunnerSpec) { s.Cache = "" },
		func(s *box.RunnerSpec) { s.Cache = "data/cache" },
		func(s *box.RunnerSpec) { s.Cache = "/data/cache,destination=/etc" },
		func(s *box.RunnerSpec) { s.Size = contract.BoxSize{Name: "huge"} },
	} {
		spec := testSpec()
		change(&spec)
		if err := h.StartRunner(context.Background(), spec); err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
}

func TestEgressAloneHasHostNetworkAndNoCapabilities(t *testing.T) {
	sum := strings.Repeat("a", 64)
	args := strings.Join(egressArgs("image", "/data/bin", sum), " ")
	for _, want := range []string{"--network=host", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user 1001:1001", "/data/bin/" + sum + ":/opt/dyna/bin/current:ro", "ci-egress --dir /run/dyna-egress"} {
		if !strings.Contains(args, want) {
			t.Errorf("args miss %q: %s", want, args)
		}
	}
}

func TestListRunnersSeparatesTheProxy(t *testing.T) {
	h, runner := newFrameHost(t)
	runner.reply = []byte(`[
{"Names":["dyna-ci-pve-1"],"State":"running","Labels":{"dyna.ci":"runner","dyna.ci.repo":"owner/repo","dyna.ci.id":"42","dyna.ci.slot":"3","dyna.ci.size":"medium"}},
{"Names":["dyna-ci-pve-2"],"State":"created","Labels":{"dyna.ci":"runner","dyna.ci.repo":"owner/repo","dyna.ci.id":"43","dyna.ci.slot":"0"}},
{"Names":["dyna-ci-egress"],"State":"running","Labels":{"dyna.ci":"egress"}}]`)
	found, egress, err := h.ListRunners(context.Background())
	if err != nil || !egress || len(found) != 2 {
		t.Fatalf("%+v %v %v", found, egress, err)
	}
	medium, _ := box.Size(contract.BoxSizeMedium)
	large, _ := box.Size(contract.BoxSizeLarge)
	if found[0] != (box.Runner{Name: "dyna-ci-pve-1", Repo: "owner/repo", ID: 42, Slot: 3, Size: medium, Running: true}) || found[1].Running || found[1].Size != large {
		t.Fatalf("%+v", found)
	}
}

func TestRunnerImageContextHasEveryFile(t *testing.T) {
	for _, name := range []string{"Containerfile", "entrypoint.sh", "job-started.sh"} {
		if _, err := images.Runner.ReadFile("ci-runner/" + name); err != nil {
			t.Fatal(err)
		}
	}
	file, err := images.Runner.ReadFile("ci-runner/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), "@sha256:") || !strings.Contains(string(file), "sha256sum -c") {
		t.Fatal("the base image and the runner download must be pinned by digest")
	}
}

// hook runs the job-started hook under a stand-in for the runner's worker. Like
// the real worker, it goes on to the next step when the hook fails: a step with
// if: always(), or an action's pre step, which is on by default. The only way to
// stop that step is to kill the worker, and that is what a refusal must do.
func hook(t *testing.T, event, payload, repo string) (string, error) {
	t.Helper()
	data, err := images.Runner.ReadFile("ci-runner/job-started.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, data, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "event.json")
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `bash -e "$1"; echo the next step ran`, "worker", script)
	cmd.Env = append(os.Environ(), "GITHUB_REPOSITORY="+repo, "DYNA_CI_REPO=owner/repo", "GITHUB_EVENT_NAME="+event, "GITHUB_EVENT_PATH="+path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The runner treats a hook it cannot start as one failed step, so an image with a
// broken hook must not reach a job at all.
func TestARunnerWithABrokenHookTakesNoJob(t *testing.T) {
	data, err := images.Runner.ReadFile("ci-runner/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(script, data, 0700); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.sh")
	if err := os.WriteFile(broken, []byte("if then\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{broken, filepath.Join(dir, "missing.sh")} {
		home := t.TempDir()
		cmd := exec.Command("bash", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "ACTIONS_RUNNER_HOOK_JOB_STARTED=" + hook}
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("%s: the runner started: %s", hook, out)
		}
		if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
			t.Fatalf("%s: the entrypoint went on: %v %v", hook, entries, err)
		}
	}
}

func TestJobStartedHookKillsTheWorkerOfARefusedJob(t *testing.T) {
	same := `{"pull_request":{"head":{"repo":{"full_name":"owner/repo"}}}}`
	for _, test := range []struct {
		name, event, payload, repo string
		ok                         bool
	}{
		{"push", "push", `{}`, "owner/repo", true},
		{"manual", "workflow_dispatch", `{}`, "owner/repo", true},
		{"own pull request", "pull_request", same, "owner/repo", true},
		{"fork pull request", "pull_request", `{"pull_request":{"head":{"repo":{"full_name":"attacker/repo"}}}}`, "owner/repo", false},
		{"deleted fork", "pull_request", `{"pull_request":{"head":{"repo":null}}}`, "owner/repo", false},
		{"unreadable event", "pull_request", `not json`, "owner/repo", false},
		{"pull_request_target", "pull_request_target", same, "owner/repo", false},
		{"workflow_run", "workflow_run", same, "owner/repo", false},
		{"issue comment", "issue_comment", `{}`, "owner/repo", false},
		{"another repository", "push", `{}`, "other/repo", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel() // each refusal waits for its log line to reach GitHub
			out, err := hook(t, test.event, test.payload, test.repo)
			if test.ok {
				if err != nil || !strings.Contains(out, "the next step ran") {
					t.Fatalf("refused: %v: %s", err, out)
				}
				return
			}
			if strings.Contains(out, "the next step ran") {
				t.Fatalf("a step ran after the refusal: %s", out)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the worker survived: %v: %s", err, out)
			}
		})
	}
}

func TestPruneRunnerFoldersProtectsExistingContainers(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint("listing-failed-", failed), func(t *testing.T) {
			root := t.TempDir()
			for _, slot := range []string{"0", "1", "2", "other"} {
				if err := os.Mkdir(filepath.Join(root, slot), 0755); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			// Both running and stopped containers appear in ps --all. Named volumes
			// must never be inspected or deleted by this operation.
			fake := `#!/bin/bash
case "$1" in
ps)
 [[ "$*" == *--all* ]] || exit 9
 [[ "$FAIL_LIST" != true ]] || exit 1
 printf '0\n2\n'
 ;;
unshare) shift; exec "$@" ;;
*) exit 9 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(fake), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", pruneRunnerFoldersScript, "test", root, "16")
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), fmt.Sprint("FAIL_LIST=", failed))
			out, err := cmd.CombinedOutput()
			if (err != nil) != failed {
				t.Fatalf("error = %v: %s", err, out)
			}
			for _, slot := range []string{"0", "1", "2", "other"} {
				_, err := os.Stat(filepath.Join(root, slot))
				wantGone := slot == "1" && !failed
				if os.IsNotExist(err) != wantGone {
					t.Errorf("slot %s: stat = %v", slot, err)
				}
			}
		})
	}
}

func TestRunnerSurveyAndRemovalPruneFolders(t *testing.T) {
	for _, remove := range []bool{false, true} {
		h, runner := newFrameHost(t)
		runner.reply = []byte("[]")
		var err error
		if remove {
			err = h.RemoveRunner(context.Background(), "dyna-ci-test")
		} else {
			_, _, err = h.ListRunners(context.Background())
		}
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, args := range runner.args {
			if strings.Contains(strings.Join(args, " "), "dyna-ci-prune") {
				found = true
			}
		}
		if !found {
			t.Fatalf("remove=%v did not prune", remove)
		}
	}
}
