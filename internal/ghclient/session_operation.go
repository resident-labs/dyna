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
	"strconv"
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
	Stdout        []byte       `json:"stdout,omitempty"`
	Stderr        []byte       `json:"stderr,omitempty"`
	ExitCode      int          `json:"exitCode"`
	Local         bool         `json:"local,omitempty"`
	TrackUpstream *UpstreamRef `json:"trackUpstream,omitempty"`
}

// UpstreamRef is the tracking configuration a successful push with
// push.autoSetupRemote records, the local half of what git would do.
type UpstreamRef struct {
	Branch string `json:"branch"`
	Remote string `json:"remote"`
	Merge  string `json:"merge"`
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
// in this room, from an isolated repository the engine builds. Local reports
// that the worker's real git should handle it, without a credential.
func RunSessionGitPush(ctx context.Context, rec room.Record, request CommandRequest) (CommandResult, error) {
	if err := validateCommandRequest(request); err != nil {
		return CommandResult{}, err
	}
	ctx = WithRoom(ctx, rec.ID)
	if Access(ctx, rec) != ReadWrite {
		return CommandResult{}, errors.New("GitHub writes are disabled: this room is read-only")
	}
	plan := parseGitPush(request.Args)
	if !plan.push || !plan.safe {
		return CommandResult{Local: true}, nil
	}
	r, ok := sessionPushRepository(ctx, rec, request.Dir, &plan)
	if !ok {
		return CommandResult{Local: true}, nil
	}
	// Targets resolve against the worker repository before the push; the push
	// itself runs from a bare repository the engine owns, so no worker
	// configuration is read between validation and execution.
	targets, track, ok := planPushTargets(ctx, request.Dir, plan)
	if !ok {
		return CommandResult{Local: true}, nil
	}
	ctx = WithRepository(ctx, r.host, r.name)
	result, err := runIsolatedPush(ctx, r, request.Dir, plan, targets)
	if err == nil && track != nil && result.ExitCode == 0 {
		result.TrackUpstream = track
	}
	return result, err
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
	plan := parseGitPush(args)
	if !plan.push {
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
	if result.TrackUpstream != nil && result.ExitCode == 0 {
		if err := setBranchUpstream(ctx, launchEnv, result.TrackUpstream); err != nil {
			return err
		}
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

func sessionPushRepository(ctx context.Context, rec room.Record, dir string, plan *pushPlan) (repository, bool) {
	if !plan.push || !plan.safe {
		return repository{}, false
	}
	remote := plan.remote
	if remote == "" {
		remote = defaultPushRemote(ctx, dir)
		plan.remote = remote
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

// pushPlan is the parsed form of a worker push: the flags an engine push
// reproduces, the refspecs it carries, and whether the command may run with
// engine credentials. Unknown or translated-away forms are never safe, so
// they stay local, where the worker's git handles them without a credential.
type pushPlan struct {
	remote   string
	push     bool
	safe     bool
	force    bool
	dryRun   bool
	atomic   bool
	quiet    bool
	delete   bool
	refspecs []string
	leases   []string
	options  []string
}

// parseGitPush identifies a push and whether its arguments are safe to run
// with engine credentials. Unknown forms stay local.
func parseGitPush(args []string) pushPlan {
	plan := pushPlan{safe: true}
	global := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if global {
			switch {
			case arg == "push":
				plan.push = true
				global = false
			case arg == "-C" || arg == "--git-dir" || arg == "--work-tree":
				// The engine validates the directory the guard runs in. A
				// repository chosen on the command line can differ, so its
				// configuration must never enter an engine push.
				plan.safe = false
				i++
			case strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree=") ||
				strings.HasPrefix(arg, "-C"):
				plan.safe = false
			case arg == "-c" || arg == "--config-env":
				plan.safe = false
				i++
			case arg == "--no-pager" || arg == "--bare" || arg == "--no-replace-objects" ||
				arg == "--literal-pathspecs":
			default:
				return pushPlan{}
			}
			continue
		}
		switch {
		case arg == "--":
			plan.refspecs = append(plan.refspecs, args[i+1:]...)
			i = len(args)
		case arg == "--repo" || arg == "-o" || arg == "--push-option":
			if i+1 >= len(args) {
				return pushPlan{push: true}
			}
			i++
			if arg == "--repo" {
				if plan.remote != "" {
					return pushPlan{push: true, safe: false}
				}
				plan.remote = args[i]
			} else {
				plan.options = append(plan.options, args[i])
			}
		case strings.HasPrefix(arg, "--repo="):
			if plan.remote != "" {
				return pushPlan{push: true, safe: false}
			}
			plan.remote = strings.TrimPrefix(arg, "--repo=")
		case strings.HasPrefix(arg, "--push-option="):
			plan.options = append(plan.options, strings.TrimPrefix(arg, "--push-option="))
		case strings.HasPrefix(arg, "-o") && len(arg) > 2:
			plan.options = append(plan.options, arg[2:])
		case arg == "--delete" || arg == "-d":
			plan.delete = true
		case arg == "--force" || arg == "-f":
			plan.force = true
		case arg == "--atomic":
			plan.atomic = true
		case arg == "--dry-run" || arg == "-n":
			plan.dryRun = true
		case arg == "--quiet" || arg == "-q":
			plan.quiet = true
		case strings.HasPrefix(arg, "--force-with-lease="):
			lease := strings.TrimPrefix(arg, "--force-with-lease=")
			if lease == "" {
				return pushPlan{push: true, safe: false}
			}
			plan.leases = append(plan.leases, lease)
		case arg == "--no-force-with-lease" || arg == "--no-force-if-includes" ||
			arg == "--thin" || arg == "--no-thin" ||
			arg == "--verbose" || arg == "-v" || arg == "--progress" || arg == "--no-progress":
		case arg == "--force-with-lease":
			// A lease without its ref covers every pushed ref.
			return pushPlan{push: true, safe: false}
		case arg == "--force-if-includes":
			// The engine resolves leases itself, which would silently drop the
			// include check this flag adds to a lease taken from a tracking
			// ref. The local push keeps the exact behavior.
			return pushPlan{push: true, safe: false}
		case !strings.HasPrefix(arg, "-"):
			if plan.remote == "" {
				plan.remote = arg
			} else {
				plan.refspecs = append(plan.refspecs, arg)
			}
		default:
			return pushPlan{push: true, safe: false}
		}
	}
	return plan
}

// pushTarget is one refspec of an engine push: the object the worker
// repository holds and the full destination ref to create, move or delete.
type pushTarget struct {
	src    string
	dst    string
	delete bool
	force  bool
}

// planPushTargets resolves a push plan against the worker repository: which
// objects to send and which remote refs to update. Resolution only reads the
// repository; the push itself runs elsewhere. A plan that does not resolve
// stays local, where the worker's git reports the reason. The returned
// upstream, when not nil, is tracking configuration to record after a
// successful push, the local half of push.autoSetupRemote.
func planPushTargets(ctx context.Context, dir string, plan pushPlan) ([]pushTarget, *UpstreamRef, bool) {
	var targets []pushTarget
	add := func(spec string) bool {
		force := strings.HasPrefix(spec, "+")
		spec = strings.TrimPrefix(spec, "+")
		if spec == "" || strings.Contains(spec, "*") {
			return false
		}
		src, dst, hasDst := strings.Cut(spec, ":")
		if !hasDst {
			dst = src
		}
		if dst == "" || dst == "HEAD" {
			return false
		}
		if plan.delete || src == "" {
			targets = append(targets, pushTarget{dst: fullRef(dst), delete: true})
			return true
		}
		sha, err := repoid.Git(ctx, dir, "rev-parse", "--verify", src)
		if err != nil {
			return false
		}
		targets = append(targets, pushTarget{src: sha, dst: fullRef(dst), force: force})
		return true
	}
	for _, spec := range plan.refspecs {
		if !add(spec) {
			return nil, nil, false
		}
	}
	var track *UpstreamRef
	if len(targets) == 0 && !plan.delete {
		specs, upstream, ok := defaultPushSpecs(ctx, dir, plan)
		if !ok {
			return nil, nil, false
		}
		track = upstream
		for _, spec := range specs {
			if !add(spec) {
				return nil, nil, false
			}
		}
	}
	return targets, track, true
}

// defaultPushSpecs is what a push without refspecs sends: the refspecs
// remote.<name>.push configures, or one refspec for the configured push.default
// mode. An ok of false keeps the push local, where git applies the configured
// behavior without a credential. The returned upstream, when not nil, is
// tracking configuration to record after a successful push.
func defaultPushSpecs(ctx context.Context, dir string, plan pushPlan) ([]string, *UpstreamRef, bool) {
	if configured, err := repoid.Git(ctx, dir, "config", "--get-all", "remote."+plan.remote+".push"); err == nil {
		return strings.Split(configured, "\n"), nil, true
	}
	mode := "simple"
	if configured, err := repoid.Git(ctx, dir, "config", "--get", "push.default"); err == nil && configured != "" {
		mode = configured
	}
	branch, err := repoid.Git(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return nil, nil, false
	}
	tracked, merge := "", ""
	if value, err := repoid.Git(ctx, dir, "config", "--get", "branch."+branch+".remote"); err == nil {
		tracked = value
	}
	if value, err := repoid.Git(ctx, dir, "config", "--get", "branch."+branch+".merge"); err == nil {
		merge = value
	}
	switch mode {
	case "nothing":
		// Git itself rejects this push before any network use.
		return nil, nil, false
	case "current":
		return []string{branch + ":" + branch}, nil, true
	case "upstream", "tracking":
		if tracked != plan.remote || !strings.HasPrefix(merge, "refs/heads/") {
			return nil, nil, false
		}
		return []string{branch + ":" + merge}, nil, true
	case "simple":
		if tracked == plan.remote {
			if !strings.HasPrefix(merge, "refs/heads/") {
				return nil, nil, false
			}
			if merge != "refs/heads/"+branch {
				// Simple refuses a differently named upstream; git reports it.
				return nil, nil, false
			}
			return []string{branch + ":" + merge}, nil, true
		}
		if tracked != "" {
			// The branch tracks another remote. Simple pushes to the branch's
			// own name, like current, and keeps the existing upstream.
			return []string{branch + ":" + branch}, nil, true
		}
		if autoSetup(ctx, dir) {
			return []string{branch + ":" + branch}, &UpstreamRef{
				Branch: branch,
				Remote: plan.remote,
				Merge:  "refs/heads/" + branch,
			}, true
		}
		// Git rejects a simple push without an upstream; git reports it.
		return nil, nil, false
	default:
		// matching and future modes keep their exact behavior locally.
		return nil, nil, false
	}
}

func autoSetup(ctx context.Context, dir string) bool {
	value, err := repoid.Git(ctx, dir, "config", "--get", "push.autoSetupRemote")
	return err == nil && (value == "true" || value == "on" || value == "yes" || value == "1")
}

// setBranchUpstream records a branch's tracking configuration in the worker
// repository, the local half of push.autoSetupRemote after an engine push.
func setBranchUpstream(ctx context.Context, env []string, ref *UpstreamRef) error {
	binary := os.Getenv(GitBinaryEnv)
	if !isExecutable(binary) {
		return nil
	}
	for _, args := range [][]string{
		{"config", "branch." + ref.Branch + ".remote", ref.Remote},
		{"config", "branch." + ref.Branch + ".merge", ref.Merge},
	} {
		cmd := processguard.Command(ctx, binary, args...)
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func fullRef(ref string) string {
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

// resolveLease turns a lease into a ref:expectation pair, reading the worker
// repository's remote-tracking ref so a lease without an expectation still
// covers the same commit it would cover locally.
func resolveLease(ctx context.Context, dir, remote, lease string) string {
	ref, expect, has := strings.Cut(lease, ":")
	full := fullRef(ref)
	if has {
		return full + ":" + expect
	}
	if tracking, err := repoid.Git(ctx, dir, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+ref); err == nil {
		return full + ":" + tracking
	}
	return full
}

// runIsolatedPush pushes resolved targets from a bare repository the engine
// creates, which shares objects with the worker repository and holds nothing
// else. Worker configuration is never read: URL rewrites, credential helpers
// and hooks in the repository cannot change a push after validation.
func runIsolatedPush(ctx context.Context, r repository, dir string, plan pushPlan, targets []pushTarget) (CommandResult, error) {
	tmp, err := os.MkdirTemp("", "dyna-push-")
	if err != nil {
		return CommandResult{}, err
	}
	defer os.RemoveAll(tmp)
	if _, err := repoid.Git(ctx, "", "init", "--bare", "-q", tmp); err != nil {
		return CommandResult{}, err
	}
	common, err := repoid.Git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return CommandResult{}, err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "objects", "info"), 0o700); err != nil {
		return CommandResult{}, err
	}
	alternates := []byte(filepath.Join(common, "objects") + "\n")
	if err := os.WriteFile(filepath.Join(tmp, "objects", "info", "alternates"), alternates, 0o600); err != nil {
		return CommandResult{}, err
	}
	for i, t := range targets {
		if !t.delete {
			if _, err := repoid.Git(ctx, tmp, "update-ref", "refs/dyna/"+strconv.Itoa(i), t.src); err != nil {
				return CommandResult{}, err
			}
		}
	}
	args := []string{"push"}
	if plan.force {
		args = append(args, "--force")
	}
	if plan.atomic {
		args = append(args, "--atomic")
	}
	if plan.dryRun {
		args = append(args, "--dry-run")
	}
	if plan.quiet {
		args = append(args, "--quiet")
	}
	for _, lease := range plan.leases {
		args = append(args, "--force-with-lease="+resolveLease(ctx, dir, plan.remote, lease))
	}
	for _, option := range plan.options {
		args = append(args, "-o", option)
	}
	args = append(args, "https://"+r.host+"/"+r.name+".git")
	for i, t := range targets {
		if t.delete {
			args = append(args, ":"+t.dst)
			continue
		}
		spec := "refs/dyna/" + strconv.Itoa(i) + ":" + t.dst
		if t.force {
			spec = "+" + spec
		}
		args = append(args, spec)
	}
	cmd := Command(ctx, "git", tmp, args...)
	return captureSessionCommand(cmd, nil, func() error { return runGit(ctx, "", cmd, true) })
}
