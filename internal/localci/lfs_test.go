package localci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// A push to a CI node must not run the repository's Git LFS pre-push hook.
// The hook uploads to the bare CI remote and fails, so the push sets
// GIT_LFS_SKIP_PUSH and passes --no-verify. The checkout on the node skips
// the LFS smudge filter, so the worktree keeps pointer files.
func TestPushSkipsLFSUploadAndRepoHooks(t *testing.T) {
	var name string
	var args []string
	r := Runner{Node: &Node{Name: "hermeslab", SSH: "pip@hermeslab", SSHConnectTimeout: 60 * time.Second}}
	r.Command = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, command string, commandArgs ...string) error {
		joined := strings.Join(commandArgs, " ")
		if strings.Contains(joined, "push") {
			name, args = command, commandArgs
		}
		return nil
	}
	if err := r.exec().PushCommit(t.Context(), "/repo", "/var/tmp/cache", "abc123"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "GIT_LFS_SKIP_PUSH=1") || !strings.Contains(joined, "--no-verify") {
		t.Fatalf("push = %s %q, want GIT_LFS_SKIP_PUSH=1 and --no-verify", name, args)
	}
	if !strings.Contains(joined, "push") || !strings.Contains(joined, "pip@hermeslab:/var/tmp/cache") {
		t.Fatalf("push lost its destination: %s %q", name, args)
	}

	check := nodeCheck{id: "unit", name: "unit", host: true, inner: "true"}
	script := nodeCheckScript(Node{Name: "local"}, "/base", "/cache.git", "/run", "run", strings.Repeat("a", 40), strings.Repeat("b", 40), check, time.Minute, 0, false, DefaultActImage, "", "")
	if !strings.Contains(script, "GIT_LFS_SKIP_SMUDGE=1") || !strings.Contains(script, "worktree add") {
		t.Fatalf("checkout does not skip LFS smudge: %s", script)
	}
}

// A failed push is a job, not an empty error. The trial report otherwise says
// the job is unknown.
func TestPushFailureIsJobEvidence(t *testing.T) {
	const gitErr = "batch response: Post https://hermeslab/info/lfs connection refused"
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "hermeslab", SSH: "pip@hermeslab", Enabled: true}}
	legacy := func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " push ") || strings.HasPrefix(joined, "push ") {
			fmt.Fprint(out, gitErr)
			return errors.New("exit status 1")
		}
		if name == "git" && fakeNodeGit(dir, out, name, args) {
			return nil
		}
		if strings.Contains(joined, "df -Pk") {
			fmt.Fprint(out, "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda 1000000 1 999999 1% /var/tmp\n")
		}
		return nil
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"go-static"}, DeferSave: true})
	if err == nil {
		t.Fatal("a failed push returned no error")
	}
	if len(result.Checks) != 1 {
		t.Fatalf("checks = %+v, want one job for the failed push", result.Checks)
	}
	job := result.Checks[0]
	if job.Name() == "" || job.Status != OutcomeError || !strings.Contains(job.Error, "connection refused") {
		t.Fatalf("job = %+v, want the git error text as job evidence", job)
	}
}

// A checkout that fails before the check runs keeps the git error on that job.
// Exit 69 alone would hide the text and call it a container failure.
func TestCheckoutFailureKeepsGitError(t *testing.T) {
	const gitErr = "smudge filter lfs failed"
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "hermeslab", SSH: "pip@hermeslab", Enabled: true}}
	legacy := func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "push") || (name == "git" && fakeNodeGit(dir, out, name, args)) {
			return nil
		}
		if strings.Contains(joined, "df -Pk") {
			fmt.Fprint(out, "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda 1000000 1 999999 1% /var/tmp\n")
			return nil
		}
		if name == "systemd-run" {
			fmt.Fprintf(out, "DYNA_CI_GIT_ERROR=%s\n", gitErr)
			return exitError(t, 69)
		}
		return nil
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"go-static"}, DeferSave: true})
	if err != nil {
		t.Fatalf("a checkout failure is a job result, not a run error: %v", err)
	}
	if len(result.Checks) != 1 {
		t.Fatalf("checks = %+v, want the checkout job", result.Checks)
	}
	job := result.Checks[0]
	if job.Status != OutcomeError || job.Cause != CauseGit || !strings.Contains(job.Error, gitErr) {
		t.Fatalf("job = %+v, want cause %s and the git error", job, CauseGit)
	}
}
