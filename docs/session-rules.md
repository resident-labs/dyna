# Session rules and compaction

Dyna sends the full session rules on the first turn. Later turns send only
input and a short frame with the turn number, hierarchy, room, selectors,
and history command. Session metadata stores `rulesHash`, a SHA-256 hash of
the full shared rule text. Task text and worker context are outside the hash.
A missing or changed hash sends the current full rules once, with an update
note. This also updates sessions created before rule hashes existed.

A verified compaction record sets `rulesCompacted` in session metadata.
The next turn sends the full rules once and clears this flag. Live steering
does not refresh the rules. A later compaction sets the flag again, including
one during a rules refresh. Transfer and consolidation start fresh conversations,
reset the receipt, and send the full first-turn rules.

## Detection by harness

Verified stdout records and successful Dyna-controlled compact commands
trigger a refresh. Text that mentions compaction,
tool output, and unknown event names do not trigger one.

| Harness | Detection and evidence |
| --- | --- |
| Claude Code | Detects `{"type":"system","subtype":"compact_boundary"}` in `claude-stream-json`. The [Agent SDK streaming reference](https://code.claude.com/docs/en/agent-sdk/streaming-output) documents this message. Plain text output cannot expose it. |
| Codex | No verified signal in `codex exec --json`. The upstream [exec event types](https://github.com/openai/codex/blob/main/codex-rs/exec/src/exec_events.rs) and [JSONL processor](https://github.com/openai/codex/blob/main/codex-rs/exec/src/event_processor_with_jsonl_output.rs) do not expose a compaction event. Dyna does not treat internal app-server events as exec stdout. |
| OpenCode | No verified signal in `opencode run --format json`. The upstream [noninteractive output code](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/run/noninteractive.ts) emits text, reasoning, tools, step records, and errors, but no compaction record. Dyna-controlled `session.compact` requests wait for the service to become idle, then restore the snapshot before the resumed turn. Automatic service compaction has no verified stdout receipt. |
| Antigravity | No verified signal in the captured `internal/workeroutput/testdata/antigravity-stream.jsonl` records or its adapter. |
| Muse | No verified signal in captured `internal/workeroutput/testdata/muse-stream.jsonl` and `muse-tools.jsonl` records or its adapter. |
| Custom, Pi, mock | No verified structured compaction signal in the configured output stream. |

Sources and captures were checked on 2026-10-17. For streams without a verified
signal, automatic refresh after compaction is unavailable. Rule changes still
refresh on the next turn. Add detection only after a real record or harness
reference establishes the stdout format.

## Consolidated task context

The consolidated charter and source manifest follow the same delivery policy.
Session metadata stores `charterHash`, a SHA-256 hash of the bounded charter
and manifest text sent to the native conversation. Ordinary follow-ups omit
both. A content change resends them once. Detected compaction sets
`charterCompacted`; the next turn resends both and clears the flag. Transfer
and consolidation reset this receipt for the fresh conversation. The harness
limits above also apply to charter refreshes.

The first consolidated turn includes the planned source references. Durable
charter storage remains separate from the receipt, so recovery and
`consolidate status` retain the full charter. Consecutive model transfers use
the latest written handoff as recent input instead of nesting an older
generated transfer packet.
