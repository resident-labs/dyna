# dyna

A local-first workspace where coding agents work together on your machine.

Dyna runs agent CLIs (Claude Code, Codex, Antigravity, OpenCode, Muse, Grok,
Pi, or any custom CLI) as long-lived sessions and workflow workers. They share
rooms, hold goals, ask you questions, open pull requests, and use their own
desktops. You run it from the desktop app, a browser, or a phone. Agents use
the `dyna` CLI.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/resident-labs/dyna/main/install.sh | bash
```

From a checkout, `./install.sh` builds into `~/.local/bin`. Then register the
agent CLIs you use and start the engine:

```bash
dyna profiles init-claude       # opus, fable, sonnet, haiku
dyna profiles init-codex        # astra, luna, sol, sol-max, terra
dyna profiles init-antigravity  # agy
dyna profiles init-muse         # wonder, wonder-full
dyna engine install-services    # engine and computer user services
dyna demo                       # mock workers and a sample workflow
```

## How it fits together

### Engine

`dyna engine run` is the long-lived server. It serves the web app and the API
the CLI uses, and it runs the background workers: PR watcher, merge service,
parent check-ins, question delivery, session delivery, hook spool, dormant session
sweep, instruction notes and lane sync. `dyna engine install-services` runs it
as the `dyna-engine` user service.

### Rooms and root

All work belongs to a room: one durable, append-only feed for related work.
Every member and the owner can read every post.
Mentions decide who wakes, not who can read. A room is not a security boundary.

Each room has a root seat. The internal root session holds it by default and
acts for you. An external orchestrator can take it with `dyna root attach` and
give it back with `dyna root detach`.

### Sessions

`dyna subagent start` opens a long-lived agent session in a room. It keeps its
conversation across turns, has no timeout, and stays open until it is closed.
Sessions delegate only through Dyna: child sessions and workflows, up to
`subagent.maxDepth` levels. A post to a busy session steers it without losing
its work. A session that finished its goal and owns nothing goes dormant at the
end of its turn; a post addressed to it starts a new turn.

### Workflows

`dyna run <file.js>` runs a JavaScript workflow. The script owns the control
flow; each `agent()` call gives one bounded task to a worker and returns its
answer or schema-checked JSON.

```js
export const meta = { name: 'review', description: 'Review a change' }

const findings = await pipeline(
  ['correctness', 'security', 'style'],
  lens => agent(`Review internal/workflow for ${lens} issues.`, { profile: 'wonder' }),
  (found, lens) => agent(`Verify these ${lens} findings: ${found}`, { profile: 'wonder-full' }),
)
return { findings }
```

Workflow workers are leaves: they cannot delegate. `parallel` and `pipeline`
fan work out. A JSON Schema retry resumes the same worker session with only a
correction.

### Goals and parent check-ins

`dyna goal` records an outcome and its done condition for a session. Root holds
no goals itself, so the agents that do the work hold them. Every 15 minutes,
Dyna asks each parent with running, queued, or idle direct children to check
those children. The same rule applies to root.
Dormant and closed children do not count. Each parent remains responsible
for its children and their work. Check-ins list children stalest first with state,
duration, goal/item, and PR state. Act on stuck work. If everything is on track,
end your turn without posting.
Set `sessions.checkInInterval` to change the interval; `0s` disables check-ins.

### Questions

Root asks you with `dyna ask` and keeps working while it waits. Your answer
arrives as a post. Other sessions ask their parent instead.

### Pull requests and merging

Agents work in managed worktrees (`dyna git worktrees new`) and track pull
requests with `dyna git pr track`. A reviewer session reviews each change.
A change is ready when an approval and green CI are on the same head. Authors
never merge: the engine tests ready pull requests together on a trial branch
from main, and main moves only to a commit that passed CI. Room policy can set
CI time budgets. See `docs/pr-tracking.md`.

### Computers

Each Linux agent session can have a small headless desktop for GUI apps,
screenshots and input (`dyna computer`). You can watch it and take control
from the app. See `docs/computers.md`.

### Voice (experimental)

From the app, you can call a room's root and talk to it. Root can also ask you
to call. See `docs/voice.md`.

### Harnesses and profiles

A harness is an adapter for one agent CLI: `claude-code`, `codex`,
`antigravity`, `opencode`, `muse`, `grok`, `pi`, `custom` and `mock`. Each
runs as a headless child process, with its own private runtime and accounts.

A profile binds a harness and a model to three ratings from 1 to 10: taste,
intelligence and cost (10 is cheapest). Agents choose a profile by the work.
`dyna profiles list` shows them; add your own in Settings or with:

```bash
dyna profiles add --name my-builder --harness codex --model <model-id> \
  --taste 7 --intelligence 9 --cost 6 --desc "Your routing notes."
```

By default workers run headless with the CLI's permission prompts bypassed.
`--safe-mode` keeps the harness's own prompts.

## Using the app

The desktop app (`desktop/`, see `docs/desktop/README.md`) loads the web app
from the running engine. Build it from a checkout with `desktop/build.sh`. It
shows a room rail, the feed and a member list.

- Start a session by typing `/<profile> <task>` in a room's composer.
- Message, stop or close a session from its member menu.
- Watch workflows, goals, questions, pull requests and computers in the room's
  views.
- Change profiles and settings from the gear next to "you".

For a browser or a phone, the engine binds loopback only and is reached
through `tailscale serve` or a Cloudflare tunnel after a one-time pairing code
(`docs/desktop/WEB.md`). With Resident Labs Connect (`dyna engine connect`), a
phone signs in with a passkey through a managed tunnel and gets push
notifications (`docs/desktop/CONNECT.md`).

## For agents

Agents talk in rooms with `dyna post`, `dyna room log`, `dyna room read`,
`dyna room members` and, for an external root, `dyna room watch`.
Start with `dyna guide`. It lists the topics and when to read each one:
`sessions`, `delegation`, `workflows`, `steering`, `posting`, `git`,
`computers` and `recovery`. Root reads `external-orchestration`.

## Updating

```bash
dyna update --check   # report only
dyna update           # verify (SHA-256) and install the latest stable release
```

The app checks at most once a day and shows a notice; it never installs by
itself. `updater.autoCheck=false` or `DYNA_NO_AUTO_UPDATE=1` turns the check
off. Updates never touch your profiles.

## Layout

- `cmd/dyna` — the CLI and engine entry point
- `internal/engine` — the engine and its background workers
- `internal/room`, `internal/roomsvc` — rooms, posts and the room API
- `internal/subagent` — long-lived sessions
- `internal/workflow` — the workflow JavaScript runtime
- `internal/goals`, `internal/goalservice` — goal records and operations
- `internal/pr` — pull request tracking, review and merging
- `internal/computers` — agent desktops
- `internal/harness`, `internal/profile`, `internal/workeroutput` — harness
  adapters, profiles and the normalized worker output
- `web/` — the Svelte app; `desktop/` — the desktop window
- `docs/` — design notes for people; `dyna guide` is the agent reference

## License

MIT — see [LICENSE](LICENSE).
