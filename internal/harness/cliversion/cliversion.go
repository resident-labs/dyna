// Package cliversion runs a harness CLI's version command once and parses
// the version it reports. Health checks and update probes share it.
package cliversion

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/childenv"
)

// Sentinel errors. Errors never quote command output or environment values;
// each caller maps them to its own fixed messages.
var (
	// ErrCanceled reports a run that ended because its context did: a
	// timeout or an explicit cancellation.
	ErrCanceled = errors.New("the version command was canceled or timed out")
	// ErrFailed reports a run that did not start or exited with an error.
	ErrFailed = errors.New("the version command failed")
	// ErrNoVersion reports a run that answered without a recognizable
	// version.
	ErrNoVersion = errors.New("the version command did not report a version")
)

// versionPattern finds the version a harness reports for itself, in any of
// the output formats the supported CLIs use ("2.1.2", "v2.1.2 (build)",
// "1.4.10-R6427.1", "2.1.2+build.4"). A digit run only counts as a version
// when no letter, digit or dot touches it, so "v2.1.2" keeps its major and
// "xyz2.1.2" matches nothing. Dyna versions always have three number
// groups, so a fourth group rejects the whole token. The trailing character
// is consumed, not captured.
var versionPattern = regexp.MustCompile(`(?:^|[^0-9A-Za-z.])v?([0-9]+(?:\.[0-9]+){2}(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)(?:$|[^0-9A-Za-z.])`)

// outputLimit bounds how much of each stream Run reads back.
const outputLimit = 4096

// Parse returns the complete version one bounded output reports, or an empty
// string when it reports none.
func Parse(output []byte) string {
	match := versionPattern.FindSubmatch(output)
	if match == nil || len(match[1]) > 128 {
		return ""
	}
	return string(match[1])
}

// Run executes path with args once, bounded by timeout, and returns the
// version it reports. Output goes to unlinked temporary files, not pipes, so
// Run returns when the command exits even if a descendant still holds the
// streams open. The command runs in its own process group, and the group is
// killed when Run returns, so no descendant outlives it. A nil error means
// the reported version parsed.
func Run(parent context.Context, path string, args, env []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stdout, err := outputFile()
	if err != nil {
		return "", ErrFailed
	}
	defer stdout.Close()
	stderr, err := outputFile()
	if err != nil {
		return "", ErrFailed
	}
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = childenv.WithoutLocks(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// The scope keeps a daemon that the CLI starts (opencode serve) out of the
	// engine's unit, and killing it ends that daemon with the check.
	defer agentslice.Stop(agentslice.Wrap(cmd, "version-probe"))
	err = cmd.Run()
	// Retire descendants: the command's group outlives it otherwise.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return "", ErrCanceled
	}
	if err != nil {
		return "", ErrFailed
	}
	for _, f := range []*os.File{stdout, stderr} {
		output, _ := io.ReadAll(io.NewSectionReader(f, 0, outputLimit))
		if version := Parse(output); version != "" {
			return version, nil
		}
	}
	return "", ErrNoVersion
}

// outputFile is an unlinked temporary file for one stream of a version
// command: nothing is left behind, even after a crash. The child writes to
// it directly, so no Go writer can cap its size; the run's timeout is the
// bound, and the read uses only the first outputLimit bytes.
func outputFile() (*os.File, error) {
	f, err := os.CreateTemp("", "dyna-version-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
