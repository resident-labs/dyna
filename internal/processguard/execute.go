package processguard

import (
	"context"
	"dyna-agent/internal/childenv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Process describes a single bounded child. Execute is replaceable in tests.
type Process struct {
	Dir     string
	Args    []string
	Env     []string
	Timeout time.Duration
	Output  io.Writer
	Stderr  io.Writer
	// Bind commits launch metadata while the existing native gate is closed.
	Bind func(context.Context, Reference) error
}
type Executor func(context.Context, Process) error

// Execute kills the process group on cancellation, including command descendants.
// No lock files are passed through ExtraFiles. Clean marks inherited lock
// descriptors close-on-exec before launching the child.
func Execute(ctx context.Context, p Process) error {
	if len(p.Args) == 0 || p.Timeout <= 0 {
		return fmt.Errorf("process requires a command and a positive timeout")
	}
	if p.Dir != "" {
		if _, err := os.Stat(p.Dir); err != nil {
			// The child chdirs before it execs, and the kernel reports a failed
			// chdir as a failed exec of the binary (Command sets SysProcAttr, so
			// Go cannot rewrite it). Name the directory, so a gone checkout never
			// reads as a missing Git.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				return &fs.PathError{Op: "chdir", Path: pathErr.Path, Err: pathErr.Err}
			}
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	cmd := Command(ctx, p.Args[0], p.Args[1:]...)
	cmd.Dir, cmd.Env = p.Dir, p.Env
	cmd.Stdout, cmd.Stderr = p.Output, p.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = p.Output
	}

	var err error
	if p.Bind == nil {
		err = childenv.Clean(cmd).Run()
	} else {
		var guarded *Controller
		guarded, err = Wrap(cmd)
		if err == nil {
			err = guarded.SetCancelSignal(syscall.SIGKILL)
		}
		if err == nil {
			err = guarded.Start(func(ref Reference) error { return p.Bind(ctx, ref) })
		}
		if err == nil {
			err = guarded.Wait()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Command cancels the whole process group and bounds inherited-pipe drains.
func Command(ctx context.Context, executable string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	return cmd
}

// secretNames are the engine's credential variables and auth paths. A shell,
// workflow, hook, job or computer never inherits them: each launch gets its
// account from the session that starts it (D-414). Explicit per-launch
// credentials, such as a harness's own key for its turn, are set after the
// filter and are not listed here.
var secretNames = []string{
	// GitHub tokens (D-414).
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
	// Claude Code sign-ins and auth paths.
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_MESSAGING_TOKEN",
	// Provider API keys.
	"OPENAI_API_KEY", "OPENCODE_API_KEY", "META_API_KEY", "XAI_API_KEY",
	"KIMI_API_KEY", "KIMI_REGISTRY_API_KEY", "KIMI_CODE_PASSWORD", "KIMI_MODEL_API_KEY", "KIMI_MODEL_REASONING_KEY", "KIMI_WEB_SEARCH_API_KEY", "KIMI_WEB_FETCH_API_KEY",
	"DASHSCOPE_API_KEY", "STEPFUN_API_KEY", "MINICPM_O_AUTH_TOKEN", "GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_LIVE_API_KEY",
	"DOUBAO_API_KEY", "SEEDUPLEX_API_KEY", "VOLCENGINE_DOUBAO_API_KEY",
	// Package registry tokens.
	"NODE_AUTH_TOKEN", "NPM_TOKEN",
}

// SecretName reports whether key names an engine credential or auth path.
func SecretName(key string) bool {
	return slices.Contains(secretNames, key)
}

// WithoutSecrets returns a copy of env without the engine's credential entries.
func WithoutSecrets(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return SecretName(key)
	})
}

// githubTokenNames are the GitHub variables that a session's own launch sets.
var githubTokenNames = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}

// WithoutEngineSecrets is WithoutSecrets for a session's shell. The session's
// launch already holds its own GitHub account (or an empty value), and the git
// credential helper needs it, so the GitHub token variables stay.
func WithoutEngineSecrets(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return SecretName(key) && !slices.Contains(githubTokenNames, key)
	})
}

// MetadataEnv removes session stamps, Git routing and engine secrets from child commands.
func MetadataEnv() []string {
	var env []string
	hasGoFlags := false
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CLAUDE_CODE_SESSION_ID":
			continue
		}
		if strings.HasPrefix(key, "DYNA_") || strings.HasPrefix(key, "GIT_") || SecretName(key) {
			continue
		}
		if key == "GOFLAGS" {
			_, flags, _ := strings.Cut(entry, "=")
			entry = "GOFLAGS=" + childenv.Trimpath(flags)
			hasGoFlags = true
		}
		env = append(env, entry)
	}
	if !hasGoFlags {
		env = append(env, "GOFLAGS=-trimpath")
	}
	return env
}
