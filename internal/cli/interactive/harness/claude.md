# Watch the room in Claude Code

Set `room` to the name that `dyna room create --repo "$PWD"` prints.
Start the Monitor tool with the command `dyna room watch --lines "$room"` before other work.
Keep it armed for the whole orchestration, also while you are idle.
Each stdout line is one short post summary or a `-- since` cursor. Read every line; a delivery ends at its cursor.
The Monitor cuts long notifications and expires after 30 minutes. Re-arm it at once with `--since <post-id>` from the last since line.
Read a full post with `dyna room read "$room" <post-id>`.
