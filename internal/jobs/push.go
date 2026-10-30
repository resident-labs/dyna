package jobs

import (
	"context"
	"fmt"
	"path"
	"strings"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/shellquote"
)

// DropCIRef removes the commit ref a previous job of the same commit left on
// the node. An interrupted job can leave one that points at a missing object,
// and neither a plain nor a forced push can replace such a ref.
func DropCIRef(cache, sha string) string {
	return "git --git-dir=" + shellquote.Quote(cache) + " update-ref -d " + shellquote.Quote("refs/dyna/ci/"+sha) + " 2>/dev/null || true"
}

// PushCommit gives the node the commit under its own ref, so a second job of
// the same commit replaces whatever an interrupted first job left behind. A
// base commit, when given, goes under its own ref too: it can lie outside the
// commit's history, and a check that diffs against it needs it on the node.
func (x Executor) PushCommit(ctx context.Context, repo, cache, sha, base string) error {
	var output TailBuffer
	// A node that never cached this repository, such as the first job of a
	// repository that has no local CI, has no bare cache yet, and a push to a
	// missing repository fails. The same call creates it under the node's one
	// init lock, with the check inside the lock, so a job and a CI run never
	// see a half-made cache; it costs no extra round trip.
	q := shellquote.Quote
	dir := path.Dir(cache)
	create := "mkdir -p " + q(dir) + " && flock " + q(path.Join(dir, "init.lock")) + " bash -c " + q("[ -d "+q(cache)+" ] || git init -q --bare "+q(cache))
	drop := DropCIRef(cache, sha)
	refs := []string{sha}
	if base != "" {
		drop += "; " + DropCIRef(cache, base)
		refs = append(refs, base)
	}
	if err := x.NodeCommand(ctx, &output, &output, create+" && "+drop); err != nil {
		return fmt.Errorf("prepare the commit cache on node: %w: %s", err, &output)
	}
	dest := cache
	var args []string
	if x.Node.SSH != "" {
		dest = x.Node.SSH + ":" + cache
		// The push dials like every other node command, so it waits as long as
		// the machine's connect timeout allows, for this one command only.
		args = []string{"-c", "core.sshCommand=ssh -o BatchMode=yes -o " + machines.SSHConnectTimeoutOption(x.Node.SSHConnectTimeout)}
	}
	// --no-verify skips the repository's own pre-push hooks. A Git LFS hook
	// would upload to this bare remote and fail the push. GIT_LFS_SKIP_PUSH is
	// set on this command only: the process guard strips GIT_* from every
	// child, so the variable has to be set again for git itself.
	args = append(args, "push", "--no-verify", dest)
	for _, commit := range refs {
		args = append(args, commit+":refs/dyna/ci/"+commit)
	}
	push := append([]string{"GIT_LFS_SKIP_PUSH=1", "git"}, args...)
	if err := x.Run(ctx, repo, &output, "env", push...); err != nil {
		// A second job of the same commit can lose the race to the first one: the
		// first creates the same ref after this push reads the remote. The ref then
		// holds the commit this push wants, so the upload has succeeded. Every
		// requested ref must hold its commit; a missing base means the upload failed.
		settled := true
		for _, commit := range refs {
			var ref TailBuffer
			read := "git --git-dir=" + q(cache) + " rev-parse --verify -q refs/dyna/ci/" + commit
			if x.NodeCommand(ctx, &ref, &ref, read) != nil || strings.TrimSpace(ref.String()) != commit {
				settled = false
			}
		}
		if settled {
			return nil
		}
		return fmt.Errorf("push commit to node: %w: %s", err, &output)
	}
	return nil
}
