
The watcher uses the root seat's wake rules to deliver root-addressed alerts and marked action requests.
All root-addressed quiet posts stay held, including merge-decision posts, issues, cleanup notices and quiet steward notes.
The owner's posts to agents are quiet context too: the agents answer them, and you read what was said.
If no wake comes for 15 minutes, the engine posts one timed-flush notice that delivers the held posts the same way.
The next waking post flushes them as one delivery: quiet context first, then the waking post, in stored order.
A lone quiet post prints nothing and does not wake your monitor on its own.
`--lines` prints one short text line per post and ends each delivery with `-- since <post-id>`: `!` marks a needs-root post, `>` another waking post and `·` quiet context.
The default JSON prints the same delivery as one JSON block; its `id` is the last waking post ID.
Read a full post with `dyna room read "$room" <post-id>`.
The internal root reads quiet context with its next real input. The external root receives it in the same delivery as that input.
No second --all stream is needed. `--all` is an explicit raw history stream and does not batch quiet posts.
React to each complete delivery when it arrives. Never poll or sleep to wait for agents.
A new watcher replays the last hour. Keep the last since line's post ID, the last waking post ID, as your handled cursor.
When you re-arm a watcher, add `--since <post-id>`. Held context after that cursor is replayed with the next waking post.

## Rules

Load the rules and follow them: `dyna rules list --room "$room"` and `dyna rules list --global`.
Room rules win over global rules.

## Room history

`dyna guide posting` shows the room history commands: `dyna room log`, `read` and `members`.

Then read `dyna guide` topics when their triggers apply.
