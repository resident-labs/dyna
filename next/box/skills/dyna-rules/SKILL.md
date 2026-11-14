---
name: dyna-rules
description: Root only. Use when you add, change or remove a rule in an AGENTS.md file, or when one grows long, repeats itself or reads like a log.
dyna-audience: root
---

# Writing AGENTS.md rules

Every session reads the global and room AGENTS.md on every request and follows them, so each line
costs every session tokens and attention, and a long file gets followed less.

## What belongs

Keep a line only if an agent without it would do the wrong thing.
- **Rules:** "always" or "never", concrete and checkable: "run `go test ./engine` before a PR", not
  "test well". One bullet each, ending with who decided it and when: `(owner, 2026-11-13)`.
- **Pointers:** a path, host, command or skill everyone should know of, in one line.
- **Elsewhere:** procedures go in a skill or a `/box/shared` file; status, history and current work in
  goals and state files; how a Dyna tool behaves in its description. Overviews of a repository, or
  anything the code already shows, do not belong here.
- **Size:** under about 100 lines per file. The global file holds what is true in every room; a room's
  file holds that room's own rules.

## A rule, not a story

- Write the rule as it stands: "Merge only with an approval on the current head and green CI."
  Not: "After the incident on 11-03 we decided that…".
- Change a rule by editing its line, and delete one that no longer applies. Never add a second line
  that "replaces" the first.
- Pair each "never" with what to do instead: "Never print the token; read it into a variable
  with …". A list of bare don'ts makes agents timid.
- Before adding a line, search both files for one it overlaps, and merge the two.

## Checking a file

Read it as a new agent would. For each line, ask:
1. Is it still true?
2. Would an agent go wrong without it?
3. Is it said once, here, and nowhere else (prompt, skill, tool description)?
4. Is it a rule, or a story about one?

A line that fails any of these is cut or rewritten. Tell the owner in your next report what you
removed or rewrote, in one line per change, since the decisions in the file are hers.
