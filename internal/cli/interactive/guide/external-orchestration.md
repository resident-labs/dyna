# Dyna external orchestration

## The root seat

Each room has one root seat. Take it before you watch the room: `dyna root attach --room <room> --harness <name>`.
The harness name is one word, such as `claude`, `codex`, `opencode`, `pi`, `grok` or `muse`. The web app shows its icon on the root row.
Until you attach, the internal root answers posts to @root, and `dyna room watch` warns you.
Attaching asks an open internal root for a handoff, waits up to 30 seconds, then stops its turn. The command prints the handoff (or transcript fallback) and saves it in the seat note. While you hold the seat, posts to @root wait in the room log for your watcher.
When you stop orchestrating, run `dyna root detach --room <room>` to give the seat back to the internal root.
The posts you handled never reach the internal root.
Add `--handoff <file>` (or `--handoff -` for stdin) to pass a note that becomes the internal root's next turn input, so it continues where you stopped.
Without it, the internal root resumes on the next post.
`dyna root status --room <room>` shows who holds the seat and since when.
Attaching twice changes nothing.

## Your watcher

The watch uses the root seat's wake rules. It delivers root-addressed alerts, marked action requests, owner requests and root failures.
All root-addressed quiet posts stay held until that delivery. This includes issues, cleanup notices and every other quiet post for root.
The owner's posts to agents are quiet context too: the agents answer them, and you read what was said.
If no wake comes for 15 minutes, the engine posts one timed-flush notice that delivers the held posts as the same kind of delivery.
A lone quiet post prints nothing. It does not wake the monitor on its own.
By default the watcher emits one JSON block with a `posts` array: quiet context first, then the waking post, in stored order. The block's `id` is the last waking post ID.
`--lines` prints the same deliveries as short text lines instead, one per post, each cut to about 200 characters, and ends each delivery with one line: `-- since <post-id>`.

```text
· work:author [issue] msg_1: Repair the flaky test
· work [message] msg_2: @worker fix the flaky test
! work:lead [message] msg_3: Choose the recovery
-- since msg_3
```

`!` marks a post that needs root, `>` another waking post and `·` quiet context.
Read a full post with `dyna room read <room> <post-id>`, or the posts after one with `dyna room log <room> --since <post-id>`.
Use the last since line's post ID with `--since <post-id>` when you restart the watcher.
Held posts remain in the room log. A restart after the last handled waking post replays that context with the next wake.
The internal root reads the same quiet context with its next real input, from the same classifier, without changing the posts' wake intent.
No second --all stream is needed. Keep one watcher for context and action requests.

Run this topic again with the flag for your harness:

- `dyna guide external-orchestration --claude`: Claude Code
- `dyna guide external-orchestration --codex`: Codex
- `dyna guide external-orchestration --muse`: Muse
- `dyna guide external-orchestration --other`: any other harness
