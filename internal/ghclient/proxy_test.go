package ghclient

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/approvals"
)

func TestWorkerWriteSnapshotsBodyAndDoesNotRunGH(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.md")
	if err := os.WriteFile(body, []byte("original body"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "gh")
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch \""+marker+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ReadOnlyEnv, string(GuardWorker))
	t.Setenv(BinaryEnv, binary)
	t.Chdir(dir)
	originalRequest, originalWait := requestApproval, waitApproval
	t.Cleanup(func() { requestApproval, waitApproval = originalRequest, originalWait })
	requestApproval = func(_ context.Context, in approvals.RequestInput) (approvals.Request, error) {
		if len(in.Files) != 1 || string(in.Files[0].Data) != "original body" {
			t.Fatalf("snapshots = %+v", in.Files)
		}
		if err := os.WriteFile(body, []byte("changed body"), 0600); err != nil {
			t.Fatal(err)
		}
		return approvals.Request{ID: "A-1"}, nil
	}
	waitApproval = func(_ context.Context, _ string, id string, timeout time.Duration) (approvals.Request, error) {
		if id != "A-1" || timeout != 0 {
			t.Fatalf("wait = %q, %v", id, timeout)
		}
		return approvals.Request{ID: id, State: approvals.StatePending, At: "room:parent"}, nil
	}
	err := SessionCommand(context.Background(), []string{"pr", "create", "--repo", "owner/project", "--title", "title", "--body-file", body})
	if SessionExitCode(err) != 75 || !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("held write reached gh")
	}
}

func TestApprovalResultOutputAndExitCodes(t *testing.T) {
	code := 7
	var stdout bytes.Buffer
	err := approvalResult(approvals.Request{ID: "A-2", State: approvals.StateDone, Result: &approvals.Result{ExitCode: &code, Output: []string{"gh output"}}}, &stdout)
	if SessionExitCode(err) != 7 || stdout.String() != "gh output\n" || err.Error() != "" {
		t.Fatalf("result = %v, output %q", err, stdout.String())
	}
	denied := approvalResult(approvals.Request{ID: "A-3", State: approvals.StateDenied, Result: &approvals.Result{Actor: "room:parent", Reason: "not safe"}}, &stdout)
	if SessionExitCode(denied) != 1 || !strings.Contains(denied.Error(), "not safe") {
		t.Fatalf("denied = %v", denied)
	}
}
