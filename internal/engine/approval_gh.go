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

func ghApprovalExecutor(rooms *room.Store) approvals.ExecuteFunc {
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
		stdoutText := string(stdout.Bytes())
		output := approvalOutput(stdoutText, string(stderr.Bytes()))
		result := approvals.Result{ExitCode: &code, Output: output}
		for _, field := range strings.Fields(stdoutText) {
			if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
				result.URL = field
				break
			}
		}
		return result, runErr
	}
}

// bindApprovalFiles restores every snapshot and points the original argument
// at it. The request's argv and working directory otherwise stay unchanged.
func bindApprovalFiles(payload approvals.GHPayload) ([]string, func(), error) {
	argv := append([]string(nil), payload.Argv...)
	if len(payload.Files) == 0 {
		return argv, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "dyna-approval-files-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for i, snapshot := range payload.Files {
		path := filepath.Join(dir, strconv.Itoa(i)+"-"+filepath.Base(snapshot.Path))
		if err := os.WriteFile(path, snapshot.Data, 0600); err != nil {
			cleanup()
			return nil, nil, err
		}
		for j, arg := range argv {
			if arg == snapshot.Path {
				argv[j] = path
			} else if strings.HasPrefix(arg, "--") && strings.HasSuffix(arg, "="+snapshot.Path) {
				argv[j] = strings.TrimSuffix(arg, snapshot.Path) + path
			}
		}
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
