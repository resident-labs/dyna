package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
)

// claimComment records the session that posted a GitHub comment ID, so its
// replies route back to that session.
type claimComment func(session string, id int64) error

func ghApprovalExecutor(rooms *room.Store, claim claimComment) approvals.ExecuteFunc {
	return func(ctx context.Context, r approvals.Request) (approvals.Result, error) {
		var payload approvals.GHPayload
		if err := json.Unmarshal(r.Payload, &payload); err != nil {
			return approvals.Result{}, err
		}
		argv, cleanup, err := bindApprovalFiles(payload)
		if err != nil {
			return approvals.Result{}, err
		}
		defer cleanup()
		ctx = ghclient.WithRooms(ghclient.WithRoom(ctx, r.Room), rooms)
		cmd := ghclient.Command(ctx, "", payload.Cwd, argv...)
		stdout, stderr := boundedio.New(1024*1024), boundedio.New(64*1024)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		runErr := ghclient.RunWithAccount(ctx, cmd, nil)
		code := 0
		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
		case errors.As(runErr, &exitErr):
			code = exitErr.ExitCode()
			runErr = nil
		default:
			code = 1
		}
		var outputErr error
		if stdout.Truncated() {
			outputErr = errors.New("gh stdout exceeded 1 MiB; output is incomplete")
		}
		if stderr.Truncated() {
			outputErr = errors.Join(outputErr, errors.New("gh stderr exceeded 64 KiB; output is incomplete"))
		}
		if outputErr != nil {
			if code == 0 {
				code = 1
			}
			runErr = errors.Join(runErr, outputErr)
		}
		stdoutText, stderrText := string(stdout.Bytes()), string(stderr.Bytes())
		result := approvals.Result{
			ExitCode: &code,
			Output:   approvalOutput(stdoutText, stderrText),
			Stdout:   approvalOutput(stdoutText, ""),
			Stderr:   approvalOutput("", stderrText),
		}
		for _, field := range strings.Fields(stdoutText) {
			if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
				result.URL = field
				break
			}
		}
		if runErr == nil && code == 0 && len(payload.Argv) > 1 && payload.Argv[0] == "pr" && payload.Argv[1] == "comment" {
			if err := recordPostedComment(r.Requester, result.URL, claim); err != nil {
				return result, err
			}
		}
		return result, runErr
	}
}

// recordPostedComment reads the comment ID from the URL that gh prints, such as
// https://github.com/owner/repo/pull/7#issuecomment-123, and claims it.
func recordPostedComment(session, url string, claim claimComment) error {
	_, fragment, found := strings.Cut(url, "#issuecomment-")
	id, err := strconv.ParseInt(fragment, 10, 64)
	if !found || err != nil || id < 1 {
		return errors.New("the comment is posted, but gh printed no comment ID: Dyna could not record its routing; do not post it again")
	}
	return claim(session, id)
}

// bindApprovalFiles restores every snapshot and points the original argument
// at it. The request's argv and working directory otherwise stay unchanged.
func bindApprovalFiles(payload approvals.GHPayload) ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "dyna-approval-files-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	replacements := map[string]string{}
	for i, snapshot := range payload.Files {
		path := filepath.Join(dir, strconv.Itoa(i)+"-"+filepath.Base(snapshot.Path))
		if err := os.WriteFile(path, snapshot.Data, 0600); err != nil {
			cleanup()
			return nil, nil, err
		}
		replacements[snapshot.Path] = path
	}
	argv, err := ghclient.RewriteFileArgs(payload.Argv, replacements)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return argv, cleanup, nil
}

func approvalOutput(stdout, stderr string) []string {
	var out []string
	for _, text := range []string{stdout, stderr} {
		text = strings.TrimSuffix(text, "\n")
		if text != "" {
			out = append(out, strings.Split(text, "\n")...)
		}
	}
	return out
}
