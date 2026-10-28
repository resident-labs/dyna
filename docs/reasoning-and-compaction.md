# Reasoning and compaction output contract

Research checked on 2026-10-18 against Claude Code 2.1.302
and Pi 1.3.0, plus real Dyna session captures.
Each adapter in `internal/workeroutput` turns one harness's stdout into
normalized events. This page says what every adapter must emit for reasoning
and for context compaction. `internal/transcript` and the session DM rely on it.

## Status

This page is the target contract. Until the PRs below land, Dyna has these gaps:

- Claude runs without `--thinking-display summarized`, so most thinking text
  is hidden (#465).
- Pi emits a message's thinking after its text (#467).

Remove this section when those PRs have landed.

## Reasoning

1. A whole block is one event: `{kind: "reasoning", state: "completed", text}`.
2. A streamed block is a run of `{kind: "reasoning", state: "delta", text}`
   events in source order. There is no start event. The run ends at the next
   event that is not a reasoning delta. Readers join the deltas.
3. Emit each block once. Do not emit both the deltas and the final block.
4. `text` is the provider text, unchanged. Never emit a reasoning event with
   empty text, and never emit a whole block that is only blank. A delta may be
   blank, because it can be the space between two words. A hidden, redacted or
   signature-only block emits nothing.
5. Keep source order. When one record holds several blocks, follow block order,
   so a thought that came before a reply stays before it.
6. `id` is optional. Set it only when the source gives one. Readers do not need it.
7. When a harness shows readable reasoning only behind a flag, Dyna passes that
   flag: Claude `--thinking-display summarized`.

## Compaction

1. When the source reports that compaction started, emit
   `{kind: "status", name: "compaction", state: "started"}`. Repeats are allowed.
2. When compaction succeeded, emit `{kind: "status", name: "compaction",
   state: "completed"}` once and set `Record.Compaction`. Dyna's compaction
   hook reads that flag.
3. When compaction failed or was cancelled, emit `{kind: "status", name:
   "compaction", state: "failed", text}`. `text` holds the reason when the
   source gives one. Do not set `Record.Compaction`.
4. A harness that does not report compaction emits nothing. Dyna does not guess.
5. Set `trigger` when the source says how compaction was asked for: `auto`
   when the harness decided, `manual` when a person asked. Set
   `tokensBefore` and `tokensAfter` only when the source reports those sizes.
6. When Dyna runs the harness's compact command before a turn
   (`profile.CompactDue`), the runner writes the same status event into the
   turn's normalized output with `trigger: "dyna"` and `tokensBefore` from
   the context size that made it due. That is the only compaction Dyna owns.

## Profile knobs

The profile editor sets two of these behaviors per profile. The new profile
wizard's last step is the same editor.

- **Reasoning effort.** The harness catalog lists each model's efforts. An
  effort either carries the native args, which the editor writes into
  `extraArgs`, or names a model ID, which the editor makes the profile's model.
- **Compact at N tokens.** The profile field `compact` holds `tokens` and, for
  harnesses that count against the window, `windows`: each chain model's
  context window from the catalog. Saving fails when a chain model has no
  window. `profile.CompactNative` maps it for the model of each attempt, so a
  fallback model uses its own window:

| Harness | Native setting |
| --- | --- |
| `claude-code` | `--settings {"autoCompactWindow":N+33000}`. Claude Code 2.1.302 showed a 33K compaction buffer, and the window must be 100K to 1M, so N is 67K to 967K. The buffer is not yet verified on 2.1.304. |
| `pi` | none; Dyna runs `pi --mode rpc --session <id>` with `{"type":"compact"}` |

For Claude Code profiles without an explicit threshold, Dyna uses the owner setting `harness.compactAtTokens` (200,000 by default). Dyna clamps this default to Claude's 67,000–967,000 token range. The generated launch setting overrides native Claude settings, including profile `--settings`. Set `harness.compactAtTokens` to `0` to leave native settings unchanged. An explicit profile threshold takes precedence over the owner setting.

For an explicit Pi profile threshold, Dyna uses Pi's compact command. Its decoder reports `Record.ContextTokens`, the prompt size of
each model call. The session stores the last one, and before the next resumed
turn `profile.CompactDue` decides whether Dyna runs the compact command first.
The harness still does the compaction; Dyna only chooses when to ask. A failed
compact command becomes a `compact_failed` session event, and the turn runs.

Dyna also sets a default Pi native compaction target with `harness.compactAtTokens`.
The default is 200000 tokens; 0 disables the default cap. Dyna generates model
reserves for models in the selected account's Pi catalog on Pi 1.3.0 or later.
Unknown models and unavailable catalogs keep Pi's native defaults. Pi reports
rounded windows, so Dyna uses a conservative upper bound and may compact earlier.
The generated model reserve takes precedence over an ordinary project-wide Pi
reserve. A project model override takes precedence over Dyna's generated value.
Existing pinned sessions keep their prior generation until a fresh start. Pi may
also decline compaction when it cannot split the history.

## Rendering

The transcript turns a reasoning block into a `thought` item that shows its
first line. Consecutive thoughts fold into one line with the latest text.
A compaction becomes one `compaction` item with its state, trigger, token
counts and reason. The DM and the session view show it as one full-width
divider, for example "── Context compacted · 182k → 41k tokens · auto ·
14:02 ──", with a spinner while it runs and the reason in the error color
when it fails. A note is never used for compaction.

## Per harness

The table lists what each harness sends.

| Dialect | Reasoning source | Shape | Compaction source |
| --- | --- | --- | --- |
| `claude-stream-json` | `thinking` content block, one per assistant record | whole | `system` `status` `compacting` (started); `status` null with `compact_result: "failed"` (failed); `compact_boundary` (completed, with `compact_metadata.trigger`, `pre_tokens` and `post_tokens`) |
| `pi-json` | `thinking` blocks in `message_end`; `message_update` deltas are ignored | whole | `compaction_start` and `compaction_end` with `reason` (rate), `result.tokensBefore` and `result.estimatedTokensAfter` |
| `text` | none | none | none |

Claude omits thinking text unless `--thinking-display summarized` is set. In
Dyna captures before that flag, 28 750 of 29 264 thinking blocks had only a
signature. Claude's `system` `thinking_tokens` records are token estimates, not
reasoning text.
