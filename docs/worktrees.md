# Managed worktrees

See `dyna guide delegation`.

`dyna git worktrees new` fetches `origin` and creates the branch from `origin/main`.
Use `--base` to choose another base and `--repo` to choose the repository.
Dyna prints the path and records the session from `DYNA_SESSION`.

Session worktrees and workflow worktrees use the same root and registry.
The default root is `~/.dyna-worktrees`.
The command resolves `worktrees.root` with the owner's home, so it prints the real path.
Dyna adds a repository name and hash, then a branch name and hash.
Set another root outside the main checkout with:

```sh
dyna settings set worktrees.root /path/to/worktrees
```

Inspect every worktree of the current repository:

```sh
dyna git worktrees list
dyna git worktrees gc --dry-run
dyna git worktrees gc --dry-run --json
dyna git worktrees gc
```

`list` shows the path, branch, owner, merge status, dirty status, size, and age.
`gc` includes hand-created worktrees, including paths under the home directory and `/tmp`.
Without `--dry-run`, `gc` removes directories that pass every check below.
It reports every kept directory and its reason.

Dyna removes a worktree only when:

1. Its owning session is closed, or its owner is unknown and no file changed for at least 24 hours.
2. It has no uncommitted changes or untracked files that Git does not ignore.
3. Its HEAD is an ancestor of `origin/main`, after a successful fetch.

Dyna keeps the main checkout, bare repositories, locked trees, and trees it cannot inspect.
It never deletes the branch. It runs `git worktree prune` after cleanup.
Ignored generated files, such as `node_modules`, do not make a worktree dirty.
Their modification times still count as activity for an unknown owner.
Dyna infers hand-created tree owners from session working directories and recorded workflow paths.
Every session or workflow reference must be closed before cleanup removes that tree.
The report identifies the inferred session owner.
Dyna also keeps every tree whose recorded workflow is still running.
A finished standalone workflow counts as a closed owner.

Closing a session runs maintenance for its owned worktrees and scratch files.
Dyna reports each kept worktree quietly to its owning session as an engine
event. Repeated notices for the same owner, tree and reason are suppressed.
When the PR watcher sees a merge, maintenance checks that PR branch immediately.
An open owner gets one notice and keeps its worktree until the session closes.
A finished session is dormant but still open, so it also keeps its worktrees.
The daily sweep closes it after 7 idle days, and close cleanup then removes
its clean, merged worktrees.

The `dyna-engine` host runs maintenance when it starts and once per hour.
It checks free space each minute on the worktree root and `/tmp`.
When free space falls below `worktrees.minFreeBytes`, maintenance runs immediately.
A file system that lives in memory, such as a RAM-backed `/tmp`, is judged
against its own size instead: it is low under 15 percent free.
The alert names the path that is low.
One owner alert reports space freed and the reasons other directories were kept.
The port pool holds 1,000 blocks. Dyna posts a typed capacity alert to every room at 90% and 95% use. A high-capacity period posts each alert once, and retries a delivery that failed. Use below 90%, observed at allocation or cleanup, rearms those alerts for a later period.

The default threshold is 10 GiB. Change it with:

```sh
dyna settings set worktrees.minFreeBytes '10 GiB'
```

Workflow retention settings can keep extra trees at the end of a workflow.
They cannot bypass the cleanup safety checks.
The singular `worktree` spelling remains an alias under `dyna git`.
Use `list --run <run-id>` or `list --check` for the retained-worktree registry view.
