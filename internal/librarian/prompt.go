package librarian

import (
	"fmt"
	"strings"
)

// Goal is the librarian session's standing service goal.
func Goal(main string) string {
	return "Keep the feature map and docs of " + main + " accurate; done when Dyna closes this librarian session."
}

// Task is one run's assignment. The session may have been compacted or moved
// to another model, so the task repeats everything the run needs.
func Task(main, tree, base, target, reason string, readmeMax int) string {
	changes := "Run `dyna library changed --since " + base + "` to list changed files and their owners."
	if base == "" {
		changes = "This is the first run. Survey the repository top-down: top-level features first, then their children."
	}
	return fmt.Sprintf(`[LIBRARIAN RUN %s]
You are the librarian for %s. Your durable output is the feature map in Dyna. This conversation can be compacted or moved to another model at any time, so keep nothing important only in memory.

Why this run: %s.
Your working directory %s is a checkout at %s that Dyna owns and resets before each run. Read code here. Do not edit files here.
%s

Do these tasks in order.

1. Map. Keep one entry for each feature a reader would look up: a user-facing capability, an adapter, an integration or a service. Singletons, factories and other implementation patterns are not features.
   - id: stable kebab-case. name: what a reader calls it. kind: feature, adapter, integration or service.
   - summary: at most two short sentences.
   - A parent holds what its children share. A child states only how it differs, in --differs, when it has siblings of the same kind.
   - owns: file globs, never line numbers. Link docs with --docs and tests with --tests. Use --relates only for intentional overlap.
   - Give a feature children where readers look things up: one adapter per agent CLI, one provider per service provider, one screen per main app view, one step per pipeline stage. A child owns narrower globs than its parent.
   - After you check an entry against this commit, pin it with --verified HEAD.
   Every run, whatever woke you:
   a. Re-verify every stale entry, not only the ones this change touched. Run dyna library list --stale. For each entry, read git diff <its pin> HEAD for its owned files, fix its summary, differs and owns, then pin it with --verified HEAD.
   b. For each changed file, ask whether it adds a sub-feature: a new folder, command, screen, provider or adapter. If it does, add a child under the right parent, or update the parent when a child would be one file.
   Commands: dyna library list --tree, show <id>, add <id> --name --summary --owns [--parent] [--kind], update <id> [...] [--verified HEAD], link <id> --docs <path> | --tests <glob> | --remove-docs <path> | --remove-tests <glob>, retire <id> [--successor <id>].
2. Lint. Run dyna library lint. Fix what the map can fix: owners, globs, links, summaries, differs, pins and parents. Leave other warnings.
3. Docs, maintain mode. Read README.md and docs/. Compare them with the map and with this run's changes. Lint rule 4 lists dead links and paths that no longer exist. Correct only statements that are now false. Leave true statements, wording and structure alone, and do not add new documentation.
   When something needs a fix:
   - Create your own worktree: dyna git worktrees new librarian/docs-%s --repo %s. Edit and commit only there.
   - In that worktree, run dyna library check-readme. README.md must stay within library.readmeMaxLines (%d lines). If it is over, shorten README.md. If it is still over, do not open a pull request; say so in your summary.
   - Open the pull request with gh pr create and track it with dyna git pr track <number>. The assigned reviewer and CI check it. Keep at most one open docs pull request; push to it instead of opening another.

Rules: change the map only through dyna library. Never merge anything. Do not start subagents or workflows. Answer review findings on your docs pull request with dyna git change reply.
End the turn with a short summary: entries added, updated and retired, the docs pull request if any, and the lint warning count before and after.`, short(target), main, reason, tree, short(target), changes, short(target), main, readmeMax)
}

// SmellGoal is the standing goal of the independent code-reading pass.
func SmellGoal(main string) string {
	return "Find maintainability smells in " + main + " and file room issues; done when Dyna closes this librarian session."
}

// SmellTask assigns one code-reading pass over a merge batch.
func SmellTask(main, tree, base, target string) string {
	return fmt.Sprintf(`[LIBRARIAN SMELL RUN %s]
You are the code-reading librarian for %s. Work only in %s. Do not edit files, update the feature map, or open a pull request.

Run git diff --name-only %s..%s to list every file changed in this merge batch. Read each full changed file, not only its diff. Follow its callers and read sibling features; use dyna library list --tree and dyna library show <id> to find owners and siblings. Read enough surrounding code to confirm each finding.

Check for these maintainability problems:
- Raw system text in the UI instead of registered typed events or designed components (room rule r8).
- Loose types: any, map[string]any, interface{} for known data, or stringly-typed fields where a named type or constant fits (room rule r7).
- Two paths for one feature, duplicated helpers, or duplicated decisions.
- Decisions made by parsing address strings.
- invented senders rather than a registered system event source.
- leftovers from removed features and dead code.
- oversized files whose unrelated responsibilities make changes hard to locate or review.

File each confirmed architectural smell as a room issue:
  dyna library smell "<title>" --at <file[:line]> [--at ...] --symbols <names> --cost "<maintenance cost>" --direction "<direction, not a patch>"
Cite the code you read. Dyna refuses references to missing files, duplicate open issues, and more than %d smells a day. A refusal is final; do not reword to get past it. Do not file style preferences, speculative problems, or implementation patches.

Never merge anything. Do not start subagents or workflows. End with a short summary of files and related code read, smells filed, and useful checks that found nothing.`, short(target), main, tree, base, target, SmellsPerDay)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return strings.TrimSpace(sha)
}
