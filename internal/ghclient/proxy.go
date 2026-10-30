package ghclient

import (
	"context"
	"crypto/sha256"
	"path/filepath"

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
	if mode == GuardWorker && !isReadCommand(commandArgs(args)) {
		return Submit(ctx, roomID, dir, args)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return Run(ctx, cmd)
}

// PinnedReviewArgs returns the gh api call that posts a comment-only review
// pinned to head. The REST reviews API takes commit_id, so the approved request
// names the reviewed commit even if the branch moves during approval.
func PinnedReviewArgs(host, repo string, number int, head, body string) []string {
	return []string{"api", "--hostname", host, fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number), "--method", "POST", "--raw-field", "commit_id=" + head, "--raw-field", "event=COMMENT", "--raw-field", "body=" + body}
}

// Submit sends an agent GitHub write through the shared approval path.
func Submit(ctx context.Context, roomID, dir string, args []string) error {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	ctx = WithAgentText(WithRoom(ctx, roomID))
	cmd := Command(ctx, "", dir, args...)
	cmd.Stdin = os.Stdin
	repository := repositoryFor(ctx, cmd)
	cleanup, err := ComposeBody(ctx, cmd, repository)
	if err != nil {
		return err
	}
	defer cleanup()
	args = cmd.Args[1:]
	files, err := SnapshotFiles(args, cmd.Dir, cmd.Stdin)
	if err != nil {
		return err
	}
	paths, err := MediaPaths(args)
	if err != nil {
		return err
	}
	var media []approvals.Media
	for _, path := range paths {
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(cmd.Dir, full)
		}
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxMediaSize+1))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		data, err = sanitizeMedia(path, data)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		req := mediaPostRequest{Quiet: true, Mentions: []string{"@root"}, Text: "Media for a GitHub approval.", Claim: mediaPostClaim{Room: roomID}, Files: []mediaPostFile{{Name: name, Data: data}}}
		var result mediaPostResult
		if err := engineclient.New("", engineclient.Notices(nil)).Call(ctx, "Post", []any{req}, &result); err != nil {
			return err
		}
		if len(result.Message.Attachments) != 1 {
			return errors.New("media post did not return an attachment")
		}
		sum := sha256.Sum256(data)
		media = append(media, approvals.Media{Path: path, Name: name, PostID: result.Message.ID, AttachmentID: result.Message.Attachments[0].ID, SHA256: fmt.Sprintf("%x", sum)})
	}
	request, err := requestApproval(ctx, approvals.RequestInput{Argv: args, Cwd: cmd.Dir, Repo: repository.name, Files: files, Media: media})
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

// These structs mirror the Post RPC fields without importing notify, which depends on ghclient.
type mediaPostClaim struct{ Room string }
type mediaPostFile struct {
	Name string
	Data []byte
}
type mediaPostRequest struct {
	Quiet    bool
	Mentions []string
	Text     string
	Claim    mediaPostClaim
	Files    []mediaPostFile
}
type mediaPostAttachment struct{ ID string }
type mediaPostMessage struct {
	ID          string
	Attachments []mediaPostAttachment
}
type mediaPostResult struct{ Message mediaPostMessage }
