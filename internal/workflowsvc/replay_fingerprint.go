package workflowsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"dyna-agent/internal/mode"

	"dyna-agent/internal/plan"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workflows/bundled"
)

// attemptEnvironment names engine-injected values that change per attempt.
// A deterministic workflow must not use these values in replayed results.
type attemptEnvironment string

var attemptEnvironmentNames = []attemptEnvironment{attemptEnvironment(mode.EnvRunID)}

// replayFingerprint binds the saved inputs and every statically named child
// source. Dynamic or unresolved child references cannot be resumed safely.
func replayFingerprint(opts Options, profiles *profile.Store, env []string, worktreeHead string) (string, map[string]string, error) {
	if opts.ScriptPath != "" {
		var source []byte
		var err error
		if path, ok := strings.CutPrefix(opts.ScriptPath, "bundled:"); ok {
			source, err = bundled.FS.ReadFile(path)
		} else {
			source, err = os.ReadFile(opts.ScriptPath)
		}
		if err != nil {
			return "", nil, fmt.Errorf("read root workflow source: %w", err)
		}
		if string(source) != opts.ScriptSrc {
			return "", nil, fmt.Errorf("root workflow source changed")
		}
	}
	p := plan.Extract(opts.ScriptSrc, plan.Options{SourcePath: opts.ScriptPath, WorkDir: opts.WorkDir, WorkflowDir: opts.WorkflowDir})
	if p.Status != plan.StatusComplete {
		return "", nil, fmt.Errorf("workflow source cannot be fully resolved")
	}
	for _, diagnostic := range p.Diagnostics {
		if diagnostic.Code == "dynamic_workflow" || diagnostic.Code == "nested_unresolved" {
			return "", nil, fmt.Errorf("workflow source cannot be fully resolved: %s", diagnostic.Code)
		}
	}
	sources := make(map[string]string)
	for _, child := range p.Workflows {
		if child.Depth == 0 {
			continue
		}
		path, src, err := plan.ResolveScript(child.Ref, opts.ScriptPath, opts.WorkDir, opts.WorkflowDir)
		if err != nil {
			return "", nil, err
		}
		sources[path] = plan.SourceSHA256(src)
	}
	opts.ResumeFrom = ""
	opts.SafeReplay = false
	env = slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(attemptEnvironmentNames, attemptEnvironment(name))
	})
	seenEnv := make(map[string]bool, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || seenEnv[name] {
			return "", nil, fmt.Errorf("ambiguous environment variable %q", name)
		}
		seenEnv[name] = true
	}
	slices.Sort(env)
	input := struct {
		Options      Options
		Profiles     *profile.Store
		Environment  []string
		WorktreeHead string
		Sources      map[string]string
		OS, Arch     string
		CPUs         int
	}{opts, profiles, env, worktreeHead, sources, runtime.GOOS, runtime.GOARCH, runtime.NumCPU()}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), sources, nil
}

// replayWorktreeHead rejects a dirty or non-Git worktree rather than guessing
// which files a command or agent could read.
func replayWorktreeHead(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/git", "-c", "core.fsmonitor=false", "-C", dir, "status", "--porcelain", "--untracked-files=all")
	status, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read worktree state: %w", err)
	}
	if len(status) != 0 {
		return "", fmt.Errorf("worktree has uncommitted changes")
	}
	cmd = exec.CommandContext(ctx, "/usr/bin/git", "-C", dir, "rev-parse", "--verify", "HEAD")
	head, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read worktree head: %w", err)
	}
	return strings.TrimSpace(string(head)), nil
}
