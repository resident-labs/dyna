package jobs

import (
	"context"
	"fmt"

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
// the same commit replaces whatever an interrupted first job left behind.
func (x Executor) PushCommit(ctx context.Context, repo, cache, sha string) error {
	var output TailBuffer
	if err := x.NodeCommand(ctx, &output, &output, DropCIRef(cache, sha)); err != nil {
		return fmt.Errorf("clear commit ref on node: %w: %s", err, &output)
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
	args = append(args, "push", "--no-verify", dest, sha+":refs/dyna/ci/"+sha)
	push := append([]string{"GIT_LFS_SKIP_PUSH=1", "git"}, args...)
	if err := x.Run(ctx, repo, &output, "env", push...); err != nil {
		return fmt.Errorf("push commit to node: %w: %s", err, &output)
	}
	return nil
}
