# Watch the room in another harness

Set `room` to the name that `dyna room create --repo "$PWD"` prints.
Before other work, run `dyna room watch --lines "$room"` in a background or streaming tool.
Forward each complete stdout line into your context: one line per post, and `-- since <post-id>` ending each delivery.
Use one watcher.
Read a full post with `dyna room read "$room" <post-id>`, or the posts after one with `dyna room log "$room" --since <post-id>`.
If the harness wakes you only when a process exits, use `dyna room watch "$room"` instead.
Print one complete JSON line and exit: it holds all quiet posts, the waking post and its cursor.
React to that block, then re-arm with `--since <post-id>` from the block's `id`.
