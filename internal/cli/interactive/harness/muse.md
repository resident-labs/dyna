# Watch the room in Muse

Set `room` to the name that `dyna room create --repo "$PWD"` prints.
Start the Monitor tool before other work:

```text
command: dyna room watch --lines "$room"
persistent: true
wake_delay_ms: 0
```

Each delivery wakes the idle session with short lines: one per post, then the `-- since` line. Held quiet posts do not produce lines of their own.
Keep this one monitor armed for the whole orchestration.
Read a full post with `dyna room read "$room" <post-id>`.
