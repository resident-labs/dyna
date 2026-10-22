# Watch the room in Codex

Set `room` to the name that `dyna room create --repo "$PWD"` prints.
Codex has no Monitor. Run this in `functions.exec` before other work, with your room name in `cmd`.
Keep it awaited for the whole orchestration. `notify()` pushes each line into your context.
Each line is one full context-and-wake block. Call notify(line) once for that block; do not split its posts or start a second watcher.
Keep the default JSON output: notify() receives whole lines, so no monitor cuts them.

```javascript
// @exec: {"yield_time_ms": 1000, "max_output_tokens": 1000}
let r = await tools.exec_command({ cmd: "dyna room watch ROOM", yield_time_ms: 1000, max_output_tokens: 1000 });
let buf = "";
while (true) {
  if (r.output) {
    buf += r.output;
    const lines = buf.split("\n");
    buf = lines.pop() ?? "";
    for (const line of lines) if (line.trim()) notify(line);
  }
  if (!r.session_id) { text({ exit_code: r.exit_code }); break; }
  r = await tools.write_stdin({ session_id: r.session_id, chars: "", yield_time_ms: 1000, max_output_tokens: 1000 });
}
```
