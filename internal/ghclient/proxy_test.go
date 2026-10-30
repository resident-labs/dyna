package ghclient

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
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
	for _, code := range []int{0, 7} {
		var stdout, stderr bytes.Buffer
		result := &approvals.Result{ExitCode: &code, Output: []string{"gh output", "gh warning"}, Stdout: []string{"gh output"}, Stderr: []string{"gh warning"}}
		err := approvalResult(approvals.Request{ID: "A-2", State: approvals.StateDone, Result: result}, &stdout, &stderr)
		if SessionExitCode(err) != code || stdout.String() != "gh output\n" || stderr.String() != "gh warning\n" {
			t.Fatalf("code %d: result = %v, stdout %q, stderr %q", code, err, stdout.String(), stderr.String())
		}
	}
	denied := approvalResult(approvals.Request{ID: "A-3", State: approvals.StateDenied, Result: &approvals.Result{Actor: "room:parent", Reason: "not safe"}}, &bytes.Buffer{}, &bytes.Buffer{})
	if SessionExitCode(denied) != 1 || !strings.Contains(denied.Error(), "not safe") {
		t.Fatalf("denied = %v", denied)
	}
}

func TestWorkerSnapshotUsesFinalAttributedBody(t *testing.T) {
	records := setupRooms(t, "owner/project")
	const line = "Opened by an agent."
	if err := room.DefaultStore().SetAttribution(records[0].ID, line); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(records[0].Repo, "body.md")
	if err := os.WriteFile(body, []byte("original body"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(records[0].Repo, "gh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ReadOnlyEnv, string(GuardWorker))
	t.Setenv(BinaryEnv, binary)
	t.Setenv("DYNA_ROOM_ID", records[0].ID)
	originalRequest, originalWait := requestApproval, waitApproval
	t.Cleanup(func() { requestApproval, waitApproval = originalRequest, originalWait })
	requestApproval = func(_ context.Context, in approvals.RequestInput) (approvals.Request, error) {
		preview := DisplayApprovalBody(approvals.GHPayload{Argv: in.Argv, Cwd: in.Cwd, Files: in.Files})
		if !strings.HasPrefix(preview, line+"\n\noriginal body") {
			t.Fatalf("approval preview = %q", preview)
		}
		return approvals.Request{ID: "A-4"}, nil
	}
	waitApproval = func(_ context.Context, _ string, id string, _ time.Duration) (approvals.Request, error) {
		return approvals.Request{ID: id, State: approvals.StatePending}, nil
	}
	err := SessionCommand(context.Background(), []string{"pr", "create", "--title", "title", "--body-file", body})
	if SessionExitCode(err) != 75 {
		t.Fatalf("error = %v", err)
	}
}

// A published PR comment is an agent write: it must reach the approval request
// as a pr comment, not a refusal, and must not run gh before approval.
func TestPinnedReviewArgsPinAndClassifyAsAgent(t *testing.T) {
	const head = "62908e5755a767d1a3d65260fb6d11f65affcf2c"
	args := PinnedReviewArgs("github.com", "owner/project", 7, head, "feedback")
	if !slices.Contains(args, "commit_id="+head) {
		t.Fatalf("review argv does not pin the reviewed head: %q", args)
	}
	in := ClassifyInput{Host: "github.com", Repo: "owner/project", APIHost: "github.com"}
	if got := Classify(context.Background(), args, in); got != approvals.ClassAgent {
		t.Fatalf("Classify(%q) = %q, want agent", args, got)
	}
}

func TestPublishThroughApprovalHoldsPRComment(t *testing.T) {
	originalRequest, originalWait := requestApproval, waitApproval
	t.Cleanup(func() { requestApproval, waitApproval = originalRequest, originalWait })
	var sent approvals.RequestInput
	requestApproval = func(_ context.Context, in approvals.RequestInput) (approvals.Request, error) {
		sent = in
		return approvals.Request{ID: "A-7"}, nil
	}
	waitApproval = func(_ context.Context, _ string, id string, _ time.Duration) (approvals.Request, error) {
		return approvals.Request{ID: id, State: approvals.StatePending, At: "room:parent"}, nil
	}
	args := []string{"pr", "comment", "https://github.com/owner/project/pull/7", "--body", "feedback"}
	err := PublishThroughApproval(context.Background(), "room-1", t.TempDir(), args)
	if SessionExitCode(err) != 75 {
		t.Fatalf("error = %v", err)
	}
	if len(sent.Argv) < 2 || sent.Argv[0] != "pr" || sent.Argv[1] != "comment" {
		t.Fatalf("approval argv = %q", sent.Argv)
	}
}
