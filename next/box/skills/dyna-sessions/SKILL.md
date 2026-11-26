---
name: dyna-sessions
description: Use when briefing or reviewing sessions, or troubleshooting turn delivery, git, jobs or the shared build cache.
---

# Brief a child

A child session suits a stream of work that needs its own branch, asks questions, or keeps context across wakes. For many coordinated workers, use the dyna-workflows skill. The workflow result goes to its starter; root can attach a workflow to a subscription to start it on events. Workers can be steered and paused, and dyna_workflows shows any room restrictions.

For an investigation, give the question; for a lookup, the exact command. For a search, specify breadth and whether you need locations or a review. Finding excerpts locates code; it doesn't show the code is correct or that an issue is absent.

Taste is design, wording and UI judgment; diagnostics is finding the cause of a failure; intelligence is reasoning and architecture. Use cheaper profiles for reading or mechanical work, stronger ones for judgment, subject to AGENTS.md. A profile's max concurrent limits simultaneous workflow workers; if none is listed there's no cap for the profile. `dyna_usage` reports account usage.

# Review and integrate

Choose review lenses for the work: correctness, security, performance or whether it reproduces. A workflow can run one worker per lens and a final pass over their results.

An integrator uses a topic branch of its own for each PR, based on the target branch, and merges the exact commit the reviewer passed into it (`git fetch origin <branch>`, then merge the reviewed SHA): one reused branch would update the previous PR, and before a PR exists a session cannot push to a sibling's branch or open a PR from it. The approval that counts is on the integrated head. PR events go to the session that opened the PR, so the integrator passes them to the lead.

# Delivery and troubleshooting

`dyna_contacts` lists your parent and siblings and `dyna_children` lists your children. Direct owner inputs reply to the owner. Inputs within a minute of turn end batch into the next turn, except owner, sibling and ancestor posts, your own job completions, question answers and subscription wakes. Context updates are shared with sessions holding the changed section. Compaction also runs on model transfer and when a quiet box with more than 100,000 tokens is about to stop. For exact wording, recover it via history, transcript or the original file, not the summary.

The engine retries a push or fetch once on GitHub 5xx (also `commit_refs` errors for a push) when the request body is at most 1 MiB. The second failure names the cause.

Box commands run at low priority (`nice 19`); host runs don't. A host run survives disconnects and engine restarts, and its job still receives output and the exit code. If the restarted engine can't follow it, the failure names the restart, host and unit.

`/box/shared` is shared by boxes in the room. `/box/cache` is shared by rooms and CI on the host. Configured Cargo, Go and pnpm caches share downloads and builds; Rust's compiler cache uses `sccache`. For a first large Rust build, `CARGO_INCREMENTAL=0` lets the workspace crates hit that cache too.
