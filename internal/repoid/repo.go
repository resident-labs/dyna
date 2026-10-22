// Package repoid shares repository identity between landing and lifecycle hooks.
package repoid

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/processguard"
)

// Git runs bounded Git commands without inherited session stamps or Git routing.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := GitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// GitRaw is Git without trimming, for NUL-delimited output whose first or
// last name may start or end with a space.
func GitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	var out bytes.Buffer
	// Execute's environment must omit Git routing and session stamps.
	env := processguard.MetadataEnv()
	err := processguard.Execute(ctx, processguard.Process{Dir: dir, Args: append([]string{"/usr/bin/git"}, args...), Env: env, Timeout: 10 * time.Second, Output: &out})
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

type Repo struct{ ID, Root, Main string }

// Resolve uses the effective origin URL, or another remote if origin is absent.
// Only a repository without any remote uses its first root commit as identity.
func Resolve(ctx context.Context, dir string) (Repo, error) {
	var r Repo
	var rootErr error
	r.Root, rootErr = Git(ctx, dir, "rev-parse", "--show-toplevel")
	trees, err := Git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		if rootErr != nil {
			return r, rootErr
		}
		return r, err
	}
	for _, field := range strings.Split(trees, "\x00") {
		path, ok := strings.CutPrefix(field, "worktree ")
		if !ok {
			continue
		}
		if rootErr != nil {
			// Local changes retain the common Git directory. Resolve a live
			// worktree here so every hook entry point uses the same identity.
			root, err := Git(ctx, path, "rev-parse", "--show-toplevel")
			if err != nil {
				continue
			}
			r.Root, rootErr = root, nil
		}
		r.Main = path
		break
	}
	if rootErr != nil {
		return r, rootErr
	}
	remotes, err := Git(ctx, r.Root, "remote")
	if err != nil {
		return r, err
	}
	if remotes != "" {
		name := strings.Fields(remotes)[0]
		for _, n := range strings.Fields(remotes) {
			if n == "origin" {
				name = n
			}
		}
		remote, e := Git(ctx, r.Root, "remote", "get-url", name)
		if e != nil {
			return r, e
		}
		if remote == "" || strings.ContainsAny(remote, "\r\n") {
			return r, fmt.Errorf("remote must have one nonempty URL")
		}
		r.ID, err = RemoteIdentity(r.Root, remote)
	} else {
		var roots string
		roots, err = Git(ctx, r.Root, "rev-list", "--max-parents=0", "HEAD")
		if err == nil {
			all := strings.Fields(roots)
			sort.Strings(all)
			r.ID = all[0]
		}
	}
	if err != nil {
		return r, err
	}
	return r, nil
}
