Do this first:
1. Find the room: `dyna room create --repo "$PWD"`.
2. Take the room's root seat: `dyna root attach --room <room> --harness <name>`, where the name is one word: `claude`, `codex`, `opencode`, `pi`, `grok` or `muse`. Until you do, the internal root answers posts to @root. Run `dyna root detach --room <room>` when you stop orchestrating.
3. Read `dyna guide external-orchestration` with the flag for your harness: `--claude`, `--codex`, `--muse` or `--other`. It shows how to arm your watcher and load the rules.

Topics:
