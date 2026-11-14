---
name: dyna-rules
description: Root only. Use when you add, change or remove a rule in an AGENTS.md file, or when one grows long, repeats itself or reads like a log.
dyna-audience: root
---

# Writing AGENTS.md rules

Every session reads the global and room AGENTS.md on every request. Agents follow what they say, so a
line that is not needed still costs tokens and attention on every task. Studies found that overviews and
auto-written files lower success and raise cost by about 20%. Short, specific, human-chosen rules help.
(ETH Zurich, "Evaluating AGENTS.md", 2025; Augment Code and GitHub studies of real files.)

## A rule, not a log

AGENTS.md holds what is true now, not how it came to be.
- Write the rule as it stands: "Merge only with an approval on the current head and green CI."
  Not: "After the incident on 11-03 we decided that…".
- End it with who decided it and when: `(owner, 2026-11-13)`. Keep that one tag, not the story.
- A changed rule is edited in place. A rule that no longer holds is deleted. Never add a second line
  that "replaces" the first.
- Incidents, reasons, status and history go to STATE.md, a design file or a goal, never here.

## What earns a line

Keep a line only if an agent without it would do the wrong thing.
- **Rules:** concrete and checkable. "Run `go test ./engine` before a PR", not "test well".
- **Pairs:** pair each "never" with what to do instead. "Never print the token; read it into a
  variable with …". A list of bare don'ts makes agents timid.
- **Pointers:** to a skill, a `/box/shared` file, a host or a command, in one line. The procedure lives
  there, not here.
- **Out:** overviews of a repository, how a Dyna tool behaves (its description says so), current
  settings (a live section shows them), anything an agent can find by reading the code.

## Shape

- One bullet per rule, one to three lines. Sub-bullets only for a rule with parts that must stay
  together.
- Under about 100 lines per file, and the shorter the better. The global file holds what is true in
  every room; a room's file holds that room's own rules only.
- Before adding a line, search both files for one it overlaps, and merge them.

## Checking a file

Read it as a new agent would. For each line, ask:
1. Is it still true?
2. Would an agent go wrong without it?
3. Is it said once, here, and nowhere else (prompt, skill, tool description)?
4. Is it a rule, or a story about one?

A line that fails any of these is cut or rewritten. Tell the owner in your next report what you
removed or rewrote, in one line per change, since the decisions in the file are hers.
