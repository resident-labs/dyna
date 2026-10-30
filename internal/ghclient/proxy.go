package ghclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/engineclient"
)

var requestApproval = func(ctx context.Context, in approvals.RequestInput) (approvals.Request, error) {
	var request approvals.Request
	err := engineclient.New("", engineclient.Notices(nil)).Call(ctx, "ApprovalRequest", []any{in}, &request)
	return request, err
}

var waitApproval = func(ctx context.Context, room, id string, timeout time.Duration) (approvals.Request, error) {
	var request approvals.Request
	err := engineclient.New("", engineclient.Notices(nil)).Call(ctx, "ApprovalWait", []any{room, id, timeout}, &request)
	return request, err
}

type sessionExitError struct {
	code    int
	message string
}

func (e *sessionExitError) Error() string { return e.message }

// SessionExitCode preserves gh's result and uses EX_TEMPFAIL for a held write.
func SessionExitCode(err error) int {
	if err == nil {
		return 0
	}
	var held *sessionExitError
	if errors.As(err, &held) {
		return held.code
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 1
}

// SessionCommand runs reads directly and holds every worker write for approval.
func SessionCommand(ctx context.Context, args []string) error {
	mode := GuardMode(os.Getenv(ReadOnlyEnv))
	if mode != GuardReadOnly && mode != GuardWorker {
		return fmt.Errorf("invalid GitHub guard mode")
	}
	binary := os.Getenv(BinaryEnv)
	if !isExecutable(binary) {
		return fmt.Errorf("the GitHub guard has no real gh binary")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	roomID := os.Getenv("DYNA_ROOM_ID")
	if roomID != "" {
		ctx = WithRoom(ctx, roomID)
	}
	ctx = WithAgentText(ctx)
	cmd := Command(ctx, binary, dir, args...)
	if mode == GuardWorker && Classify(ctx, args, ClassifyInput{}) != approvals.ClassRead {
		return sessionApproval(ctx, cmd, roomID, args)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return Run(ctx, cmd)
}

// PublishThroughApproval sends one agent-class GitHub write, such as a PR
// comment, through the approval path that gh writes in a session use. The
// room's root approves it at once; a child waits for its parent.
func PublishThroughApproval(ctx context.Context, roomID, dir string, args []string) error {
	ctx = WithRoom(ctx, roomID)
	ctx = WithAgentText(ctx)
	cmd := Command(ctx, "", dir, args...)
	return sessionApproval(ctx, cmd, roomID, args)
}

func sessionApproval(ctx context.Context, cmd *exec.Cmd, roomID string, args []string) error {
	cmd.Stdin = os.Stdin
	if err := refuseOwnerIdentity(ctx, cmd); err != nil {
		return err
	}
	repository := repositoryFor(ctx, cmd)
	cleanup, err := withAttribution(ctx, cmd, repository)
	if err != nil {
		return err
	}
	defer cleanup()
	args = cmd.Args[1:]
	files, err := SnapshotFiles(args, cmd.Dir, cmd.Stdin)
	if err != nil {
		return err
	}
	request, err := requestApproval(ctx, approvals.RequestInput{Argv: args, Cwd: cmd.Dir, Repo: repository.name, Files: files})
	if err != nil {
		return err
	}
	result, err := waitApproval(ctx, roomID, request.ID, 0)
	if err != nil {
		return err
	}
	return approvalResult(result, os.Stdout, os.Stderr)
}

func approvalResult(request approvals.Request, stdout, stderr io.Writer) error {
	result := request.Result
	switch request.State {
	case approvals.StateDone, approvals.StateFailed:
		if result != nil {
			stdoutLines, stderrLines := result.Stdout, result.Stderr
			if stdoutLines == nil && stderrLines == nil {
				stdoutLines = result.Output
			}
			for _, line := range stdoutLines {
				fmt.Fprintln(stdout, line)
			}
			for _, line := range stderrLines {
				fmt.Fprintln(stderr, line)
			}
			code := 1
			if result.ExitCode != nil {
				code = *result.ExitCode
			}
			if code != 0 || request.State == approvals.StateFailed {
				return &sessionExitError{code: code, message: result.Reason}
			}
			return nil
		}
	case approvals.StateDenied:
		return fmt.Errorf("GitHub write denied by %s: %s (%s)", result.Actor, result.Reason, request.ID)
	case approvals.StateExpired, approvals.StateCancelled:
		return fmt.Errorf("GitHub write %s (%s): %s", request.State, request.ID, result.Reason)
	}
	return &sessionExitError{code: 75, message: fmt.Sprintf("GitHub write held for approval as %s (waiting on %s). You will be woken with the result; do not retry.", request.ID, request.At)}
}
