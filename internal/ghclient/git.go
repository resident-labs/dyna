package ghclient

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/shellquote"
)

// RunGit runs service-owned Git work with the same room policy and account as
// GitHub CLI requests. Callers must supply WithRepository and use Command to
// remove inherited Git overrides. Read-only rooms may fetch and inspect remote refs.
func RunGit(ctx context.Context, ghBinary string, cmd *exec.Cmd) error {
	r, ok := ctx.Value(repositoryContextKey{}).(repository)
	if !ok || r.host == "" || r.name == "" {
		return errors.New("Git operations require explicit GitHub repository scope")
	}
	var policyArgs []string
	if isGitRead(cmd.Args[1:]) {
		policyArgs = []string{"repo", "view"}
	}
	if err := checkPolicy(ctx, cmd.Dir, r, policyArgs); err != nil {
		return err
	}
	gh := Command(ctx, ghBinary, cmd.Dir)
	_, token, err := selectAccount(ctx, gh, r)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("Git operations require a saved GitHub account")
	}
	headers, err := gitAuthenticationHeaders(ctx, cmd)
	if err != nil {
		return err
	}
	// Reset inherited helpers. The token stays in the child environment and the
	// helper answers only for this repository's HTTPS URL. No credentials or
	// configuration are saved to disk, including the user's Git configuration.
	helper := "!" + shellquote.Quote(gh.Path) + " auth git-credential"
	config := [][2]string{
		{"credential.helper", ""},
		{"credential.https://" + r.host + "/" + r.name + ".helper", helper},
		{"credential.https://" + r.host + "/" + r.name + ".git.helper", helper},
		{"credential.useHttpPath", "true"},
		{"core.hooksPath", os.DevNull},
	}
	// An explicit HTTP Authorization header overrides credential helpers. Reset
	// every configured extraHeader key, including more specific URL sections.
	// Only names are read, so no saved authentication values enter our output.
	for _, key := range headers {
		config = append(config, [2]string{key, ""})
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, entry := range config {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+entry[0], "GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+entry[1])
	}
	return runWithToken(cmd, token)
}

func gitAuthenticationHeaders(ctx context.Context, cmd *exec.Cmd) ([]string, error) {
	probe := Command(ctx, "git", cmd.Dir, "config", "--null", "--name-only", "--get-regexp", `^http(\..*)?\.extraheader$`)
	probe.Env = cmd.Env
	out := boundedio.New(65536)
	probe.Stdout = out
	if err := probe.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, errors.New("cannot inspect Git HTTP authentication configuration")
		}
		return nil, nil
	}
	if out.Truncated() {
		return nil, errors.New("Git HTTP authentication configuration is too large")
	}
	return strings.Split(strings.TrimSuffix(string(out.Bytes()), "\x00"), "\x00"), nil
}

// isGitRead skips only known Git global options. Unknown forms remain writes.
func isGitRead(args []string) bool {
	for len(args) > 0 {
		switch args[0] {
		case "-C", "-c", "--git-dir", "--work-tree":
			if len(args) < 2 {
				return false
			}
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "--git-dir=") || strings.HasPrefix(args[0], "--work-tree=") {
				args = args[1:]
				continue
			}
			return args[0] == "fetch" || args[0] == "ls-remote"
		}
	}
	return false
}
