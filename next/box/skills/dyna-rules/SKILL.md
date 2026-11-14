---
name: dyna-rules
description: Root only. Use when you add, change or remove a rule in an AGENTS.md file, or when one grows long, repeats itself or reads like a log.
dyna-audience: root
---

# Writing AGENTS.md rules

The global AGENTS.md says what belongs in these files. This is how to keep them a set of rules
rather than a journal.

## A rule, not a story

- Write the rule as it stands: "Merge only with an approval on the current head and green CI."
  Not: "After the incident on 11-03 we decided that…".
- Change a rule by editing its line. Never add a second line that "replaces" the first.
- Pair each "never" with what to do instead: "Never print the token; read it into a variable
  with …". A list of bare don'ts makes agents timid.

## Before adding a line

Search both files for a line it overlaps, and merge the two.

## Checking a file

Read it as a new agent would. For each line, ask:
1. Is it still true?
2. Would an agent go wrong without it?
3. Is it said once, here, and nowhere else (prompt, skill, tool description)?
4. Is it a rule, or a story about one?

A line that fails any of these is cut or rewritten. Tell the owner in your next report what you
removed or rewrote, in one line per change, since the decisions in the file are hers.
