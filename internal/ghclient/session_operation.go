package ghclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

const maxSessionInput = 1024 * 1024

// CommandRequest is one non-interactive worker command for the engine.
type CommandRequest struct {
	Dir   string   `json:"dir"`
	Args  []string `json:"args"`
	Input []byte   `json:"input,omitempty"`
}

// CommandResult contains command output, never a credential.
type CommandResult struct {
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	ExitCode int    `json:"exitCode"`
	Local    bool   `json:"local,omitempty"`
}

// SessionExitError preserves a remote command's exit status without adding a
// second error line to its output.
type SessionExitError struct{ Code int }

func (e *SessionExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", e.Code)
}
func (e *SessionExitError) ExitCode() int { return e.Code }

// RunSessionGitHub applies the worker guard and runs gh with the engine's
// account and configuration. Its result contains no credential.
func RunSessionGitHub(ctx context.Context, rec room.Record, request CommandRequest) (CommandResult, error) {
	if err := validateCommandRequest(request); err != nil {
		return CommandResult{}, err
	}
	ctx = WithRoom(ctx, rec.ID)
	if Access(ctx, rec) == ReadWrite {
		if err := checkWorkerPolicy(request.Args); err != nil {
			return CommandResult{}, err
		}
	}
	cmd := Command(ctx, "", request.Dir, request.Args...)
	return captureSessionCommand(cmd, request.Input, func() error { return Run(ctx, cmd) })
}

// RunSessionGitPush runs only a clearly identified HTTPS push to a repository
// in this room. Local reports that the worker's real git should handle it.
func RunSessionGitPush(ctx context.Context, rec room.Record, request CommandRequest) (CommandResult, error) {
	if err := validateCommandRequest(request); err != nil {
		return CommandResult{}, err
	}
	ctx = WithRoom(ctx, rec.ID)
	if Access(ctx, rec) != ReadWrite {
		return CommandResult{}, errors.New("GitHub writes are disabled: this room is read-only")
	}
	r, ok := sessionPushRepository(ctx, rec, request.Dir, request.Args)
	if !ok {
		return CommandResult{Local: true}, nil
	}
	ctx = WithRepository(ctx, r.host, r.name)
	cmd := Command(ctx, "git", request.Dir, request.Args...)
	return captureSessionCommand(cmd, nil, func() error { return runGit(ctx, "", cmd, true) })
}

func validateCommandRequest(request CommandRequest) error {
	if !filepath.IsAbs(request.Dir) || len(request.Args) == 0 || len(request.Args) > 1024 {
		return errors.New("invalid worker command")
	}
	if len(request.Input) > maxSessionInput {
		return errors.New("worker command input is too large")
	}
	return nil
}

func captureSessionCommand(cmd *exec.Cmd, input []byte, run func() error) (CommandResult, error) {
	stdout, stderr := boundedio.New(16*1024*1024), boundedio.New(1024*1024)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), stdout, stderr
	err := run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if stdout.Truncated() || stderr.Truncated() {
		return result, errors.New("worker command output is too large")
	}
	if err == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return result, err
}

var sessionOperationCall = sessionOperation

func sessionOperation(ctx context.Context, method string, request CommandRequest) (CommandResult, error) {
	var result CommandResult
	err := engineclient.New("", engineclient.Notices(nil), engineclient.RestartWait(0)).Call(ctx, method, []any{request}, &result)
	return result, err
}

func finishSessionOperation(result CommandResult) error {
	if _, err := os.Stdout.Write(result.Stdout); err != nil {
		return err
	}
	if _, err := os.Stderr.Write(result.Stderr); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return &SessionExitError{Code: result.ExitCode}
	}
	return nil
}

func sessionInput(in *os.File) ([]byte, error) {
	info, err := in.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return nil, nil
	}
	input, err := io.ReadAll(io.LimitReader(in, maxSessionInput+1))
	if err != nil {
		return nil, err
	}
	if len(input) > maxSessionInput {
		return nil, errors.New("worker command input is too large")
	}
	return input, nil
}

// SessionGitCommand mediates pushes and forwards every other Git command to
// the real binary with the worker's original environment.
func SessionGitCommand(ctx context.Context, args []string, launchEnv []string) error {
	_, push, _ := parseGitPush(args)
	if !push {
		return runLocalGit(ctx, args, launchEnv)
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := sessionOperationCall(ctx, "GitPush", CommandRequest{Dir: dir, Args: args})
	if err != nil {
		return err
	}
	if result.Local {
		return runLocalGit(ctx, args, launchEnv)
	}
	return finishSessionOperation(result)
}

func runLocalGit(ctx context.Context, args []string, env []string) error {
	binary := os.Getenv(GitBinaryEnv)
	if !isExecutable(binary) {
		return errors.New("the Git guard has no real git binary")
	}
	cmd := processguard.Command(ctx, binary, args...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func sessionPushRepository(ctx context.Context, rec room.Record, dir string, args []string) (repository, bool) {
	remote, push, safe := parseGitPush(args)
	if !push || !safe {
		return repository{}, false
	}
	if remote == "" {
		remote = defaultPushRemote(ctx, dir)
	}
	var urls []string
	if strings.Contains(remote, "://") {
		// A repository can rewrite the URL it was given (url.<base>.insteadOf),
		// and a rewritten URL may name an external transport such as ext::, which
		// runs a repository-chosen command. The engine never pushes through a
		// rewrite: when any configured rewrite matches the target URL, the push
		// stays local, where no account credential exists.
		rewrites, err := urlRewrites(ctx, dir)
		if err != nil {
			return repository{}, false
		}
		for _, base := range rewrites {
			if strings.HasPrefix(remote, base) {
				return repository{}, false
			}
		}
		urls = []string{remote}
	} else {
		out, err := repoid.Git(ctx, dir, "remote", "get-url", "--push", "--all", remote)
		if err != nil {
			return repository{}, false
		}
		urls = strings.Split(strings.TrimSpace(out), "\n")
	}
	var selected repository
	for _, raw := range urls {
		r, ok := roomHTTPSRepository(ctx, rec, raw)
		if !ok || (selected.name != "" && selected != r) {
			return repository{}, false
		}
		selected = r
	}
	return selected, selected.name != ""
}

// urlRewrites returns the configured URL rewrite prefixes (url.<base>.
// insteadOf and url.<base>.pushInsteadOf values) of a repository.
func urlRewrites(ctx context.Context, dir string) ([]string, error) {
	raw, err := repoid.GitRaw(ctx, dir, "config", "--null", "--get-regexp", `^url\.`)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var prefixes []string
	for _, record := range strings.Split(raw, "\x00") {
		key, value, _ := strings.Cut(record, "\n")
		key = strings.ToLower(key)
		if strings.HasSuffix(key, ".insteadof") || strings.HasSuffix(key, ".pushinsteadof") {
			prefixes = append(prefixes, value)
		}
	}
	return prefixes, nil
}

func defaultPushRemote(ctx context.Context, dir string) string {
	if remote, err := repoid.Git(ctx, dir, "config", "--get", "remote.pushDefault"); err == nil && strings.TrimSpace(remote) != "" {
		return strings.TrimSpace(remote)
	}
	if branch, err := repoid.Git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		remote, err := repoid.Git(ctx, dir, "config", "--get", "branch."+strings.TrimSpace(branch)+".remote")
		if err == nil && strings.TrimSpace(remote) != "" {
			return strings.TrimSpace(remote)
		}
	}
	return "origin"
}

func roomHTTPSRepository(ctx context.Context, rec room.Record, raw string) (repository, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return repository{}, false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if strings.Count(name, "/") != 1 || strings.HasSuffix(name, "/") {
		return repository{}, false
	}
	r := repository{host: u.Host, name: name}
	return r, matchesRepository(ctx, rec, r)
}

// parseGitPush identifies a push and whether its arguments are safe to run
// with engine credentials. Unknown forms stay local.
func parseGitPush(args []string) (remote string, push, safe bool) {
	safe = true
	for len(args) > 0 {
		arg := args[0]
		switch arg {
		case "-C", "--git-dir", "--work-tree":
			if len(args) < 2 {
				return "", false, false
			}
			args = args[2:]
		case "-c", "--config-env":
			if len(args) < 2 {
				return "", false, false
			}
			safe = false
			args = args[2:]
		case "--no-pager", "--bare", "--no-replace-objects", "--literal-pathspecs":
			args = args[1:]
		default:
			if strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree=") {
				args = args[1:]
				continue
			}
			if arg != "push" {
				return "", false, false
			}
			args = args[1:]
			goto pushArgs
		}
	}
	return "", false, false

pushArgs:
	for len(args) > 0 {
		arg := args[0]
		switch arg {
		case "--repo":
			if len(args) < 2 {
				return "", true, false
			}
			return args[1], true, safe
		case "-o", "--push-option", "--receive-pack", "--exec":
			if len(args) < 2 {
				return "", true, false
			}
			args = args[2:]
		case "--all", "--mirror", "--delete", "--tags", "--follow-tags", "--atomic",
			"--dry-run", "-n", "--porcelain", "--force", "-f", "--force-if-includes",
			"--no-force-if-includes", "--force-with-lease", "--no-force-with-lease",
			"--set-upstream", "-u", "--quiet", "-q", "--verbose", "-v", "--progress",
			"--no-progress", "--prune", "--no-verify", "--verify", "--signed", "--no-signed",
			"--thin", "--no-thin":
			args = args[1:]
		default:
			if strings.HasPrefix(arg, "--repo=") {
				return strings.TrimPrefix(arg, "--repo="), true, safe
			}
			if strings.HasPrefix(arg, "--push-option=") || strings.HasPrefix(arg, "--receive-pack=") ||
				strings.HasPrefix(arg, "--exec=") || strings.HasPrefix(arg, "--force-with-lease=") ||
				strings.HasPrefix(arg, "--recurse-submodules=") || (strings.HasPrefix(arg, "-o") && len(arg) > 2) {
				args = args[1:]
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return "", true, false
			}
			return arg, true, safe
		}
	}
	return "", true, safe
}
