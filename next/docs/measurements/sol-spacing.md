# Sol spacing: baseline and follow-up

## Status

**Before/after effectiveness: not measured yet.** This PR is a prompt-only mitigation requested by the owner for missing spaces already present in raw Responses SSE (upstream openai/codex#45021). Synthetic tests prove selection, delivery and removal of the instruction, not that the model follows it. No output is rewritten.

The original fork-review-lead evidence file `/tmp/frl-tail.jsonl` is no longer available: its box volume was deleted. The baseline below uses three different, current horizon session excerpts supplied through `/box/shared/frl/` on 2026-11-05. These were copied before this mitigation was deployed. We do not know whether earlier conversational reminders about spacing are in their contexts. Do not describe these excerpts as a reconstruction of the lost failure or a controlled experiment.

## Reproduce

From the repository root, with Node (tested on 22.23.4; no dependencies, service or dictionary):

```sh
node --test next/scripts/spacing-count.test.mjs
node next/scripts/spacing-count.mjs --allow-truncated-first-line \
  /box/shared/frl/fork-lead.jsonl \
  /box/shared/frl/fork-review.jsonl \
  /box/shared/frl/particle-followup.jsonl > /tmp/sol-spacing-baseline.json
# Optional sol-only cohort:
node next/scripts/spacing-count.mjs --allow-truncated-first-line --model gpt-6.1-sol \
  /box/shared/frl/fork-lead.jsonl
```

The aggregate-only result is committed as [sol-spacing-baseline.json](sol-spacing-baseline.json), including each input's byte count and SHA-256, message counts, model/provider, timestamps, counts and rates. Raw session files are not committed. The script never prints assistant prose or tool arguments, including in parse errors. It prints paths and model metadata. Invalid JSON is an error except the first physical line with the explicit truncation flag; there were two such skipped initial lines here. Invalid JSON elsewhere remains an error.

## What counts

Heuristic version 1 counts three disjoint ASCII adjacent-character boundaries: letter→digit, digit→letter and lowercase→uppercase. Each such boundary is one suspicious join site, **not one proven missing space**. For example, `at8395006b` has two sites and `authorizesmoreSolsubagents ifneeded` has one. Normal `authorizes more Sol subagents if needed` has none. The all-lowercase joins in the bad example are invisible to this counter.

Denominators are Unicode code points in decoded string values, including whitespace. Assistant `text` blocks (or string content) and `toolCall.arguments` are separate totals. For arguments, walk nested objects and arrays and count only string leaves, not JSON keys, structural punctuation, numeric/boolean/null values or the serialized outer JSON. Serialized argument JSON is decoded once, so escaped quotes/newlines become their actual characters; a malformed argument JSON string is counted as raw text and flagged in `invalid_argument_json`. String leaves containing code or another serialized JSON document are not parsed again. They are model-written strings and remain counted once. We do not concatenate leaves, which would introduce artificial join sites. Text in thinking, tool results, user messages, compaction summaries and other record types is excluded. Per-file duplicate assistant record IDs are counted once. An array of content blocks takes precedence over a redundant message-level text field. The parser accepts PiG's `{type:"message", message:{role:"assistant", model, provider, content}}` and direct decoded assistant messages.

Each assistant message's own recorded `model` selects its cohort; model-change records and profile names do not. Missing model values are excluded and counted as unknown. No model is inferred across a truncation. Provider is reported separately. There were no unknown model or timestamp records, duplicate records or malformed argument JSON values in these excerpts. The baseline includes failed/partial assistant attempts if present: it measures what was written, not only successful answers. `--after` is inclusive, `--before` exclusive; unknown timestamps are excluded when a time filter is active.

### Limitations

- Hex IDs/hashes, units (`16k`), version names (`v2`), paths, code, camelCase and API/tool names produce false positives. For example `commit 8395006b and v2 and camelCase` deliberately counts three, despite being normal.
- Most lowercase word-to-word joins are missed; uppercase/acronym joins and non-ASCII boundaries are also missed. There is no language model or dictionary.
- Assistant text can itself be code; argument strings can contain copied source or quoted text. The counter does not infer authorship within a string, distinguish prose from code, or remove fenced code.
- Different tasks, code/ID density, context lengths and little assistant text make model-to-model rate comparisons misleading. Claude cohorts below are contemporaneous references, not randomized controls. Lower rates do not imply better spacing.
- Files are measured separately. If future files overlap, do not sum them without checking provenance; per-file record deduplication does not deduplicate separate excerpts.

## Baseline

UTC ranges use assistant record timestamps. Counts are the totals of all three boundary categories. The JSON has category breakdowns and full hashes.

| File / cohort | Assistant messages | First–last UTC on 2026-11-05 | Text chars / sites / per 10k | Argument string chars / sites / per 10k |
|---|---:|---|---:|---:|
| fork-lead / gpt-6.1-sol | 51 | 15:24:04.776–15:42:29.648 | 141 / 0 / 0.0000 | 38,132 / 620 / 162.5931 |
| fork-review / gpt-6.1-sol | 55 | 15:18:34.451–15:42:30.398 | 1,847 / 2 / 10.8284 | 48,497 / 1,209 / 249.2938 |
| particle-followup / gpt-6.1-sol | 25 | 15:36:22.654–15:42:31.116 | 0 / 0 / n/a | 23,619 / 162 / 68.5888 |
| fork-lead / claude-sonnet-5-5 | 76 | 14:20:53.959–15:18:36.901 | 4,111 / 40 / 97.2999 | 35,450 / 917 / 258.6742 |
| fork-review / claude-opus-5-5 | 49 | 14:57:59.083–15:15:35.244 | 5,177 / 41 / 79.1964 | 30,869 / 925 / 299.6534 |

Sol total: **131 assistant messages**, 1,988 text characters / 2 sites / **10.0604 per 10k**; 110,248 argument string characters / 1,991 sites / **180.5928 per 10k**. This is not evidence that Sol has fewer missing spaces than the Claude reference cohorts. The intentionally crude heuristic mainly sees code and IDs in this sample.

Files: fork-lead and fork-review are each 4,000,000-byte tails and each skips one partial initial line. particle-followup is 523,969 bytes and skips none. Excluded non-assistant records: 200, 190 and 60 respectively. Assistant models are known for all 256 included messages. No model filtering was applied to the committed JSON, so the Claude messages remain separately visible rather than being silently attributed to Sol.

## Prompt delivery and tests

The two sentences live once in `engine/prompt.go`, selected by OpenAI provider plus exact `gpt-6.1` or `gpt-6.1-` family ID. The invariant startup prompt is unchanged, so unrelated prompt goldens require no regeneration. Instead, `box.hello`, each dispatched turn and each workflow profile carry the resolved model's optional `modelInstructions`. This is deliberately separate from `promptVars`/the frozen session preamble: changing only the startup preamble would leave stale Sol advice after model transfer. PiG combines the model part with host context sections; it survives snapshot replacement and is removed/re-added on successful transfers. Refused/aborted transfers retain the old model's advice. A workflow worker takes its own profile's instructions, not its crew's model.

Fail-first evidence is in `/box/shared/frl/sol-spacing-red.log`, `sol-spacing-transfer-red.log`, `sol-spacing-box-red.log` and `spacing-count-red.log`. The engine tests failed for absent guidance on positive model IDs; real PiG requests failed for missing startup guidance; box tests failed for missing workflow/turn propagation. The script fixture test initially failed because the module did not exist. Passing fixtures include known bad examples, normal prose/numbers, documented ID/code false positives, nested and serialized arguments, escaping, mixed-model filtering, unknown models, truncation, duplicate IDs and text precedence.

### Mutation checks

Each mutation was applied in an isolated detached worktree of the implementation, tested, then restored. All seven were killed by a failing assertion (not a claimed hypothetical check). Logs are `/box/shared/frl/mutation-<name>.log`.

| Mutation name | Change | Failing check |
|---|---|---|
| line-absent | Return an empty string instead of the two prompt sentences | Engine positive model cases: hello, turn and workflow lack guidance |
| overbroad-matching | Use `strings.HasPrefix(model.ID, "gpt-6.1")` without provider/family delimiter | Engine rejects `gpt-6.10-sol` and Anthropic `gpt-6.1-sol` |
| no-turn-propagation | Remove `ModelInstructions` from dispatched `TurnStart` | Engine positive cases lack turn guidance |
| no-runner-propagation | Pass an empty instruction string to `TransferModel` | Runner's recorded model instructions differ |
| no-worker-propagation | Clear `workerConfig`'s profile instruction | Worker config assertion fails |
| no-transfer-guidance-update | Leave the old guidance on successful transfer | Real PiG request lacks the removal update |
| counter-no-letter-digit-detection | Disable letter→digit detection | Synthetic example, nested argument and text precedence counts differ |

Focused commands: `go test ./engine -run '^TestSolSpacingUses' -count=1`, `go test ./box/agent -run '^TestRunnerTransfersOnFirstTurn' -count=1`, `go test ./box/agent -run '^TestWorkersOpenWithTheir' -count=1`, `go test ./harness/pig -run '^TestModelInstructions' -count=1` and `node --test scripts/spacing-count.test.mjs` (from `next/`). Unmutated versions pass.

### Validation

- `go test -race` passed for each touched package, invoked separately: `./contract`, `./harness`, `./harness/fakeharness`, `./harness/pig`, `./box/agent`, `./engine`. The first full engine race run hit Go's default 10-minute suite timeout while initializing an unrelated subscription test; the complete retry `GOMAXPROCS=2 go test -race -timeout 20m ./engine -count=1` passed in 523.998 seconds. The targeted final engine guidance test also passed with `-race`.
- `GOMAXPROCS=2 go test -count=1 -timeout 3m ./...`, `go build ./...` and `go vet ./...` passed. The first broad test run failed existing Podman namespace tests because this disposable box lacked `fusermount3`; installing the genuine Debian bookworm fuse3 utility under the home directory and rerunning with it on PATH resolved that environment failure, without repository changes.
- `go generate ./contract` ran, and rerunning left the generated contract unchanged. Prompt golden files are unchanged.
- Web check and all 488 web tests passed on Node 24.22.2; Svelte reports 0 errors and 26 warnings in existing files. The web build passed (Vite reports its existing large-chunk warning).
- All five offline measurement fixture tests passed, deterministic remeasurement matched the committed JSON byte-for-byte, and `bash -n scripts/ci.sh` plus `git diff --check` passed.

## Fresh post-deploy follow-up (pending)

After deployment, allow a few hours of **fresh Sol output**, not a rerun on these old excerpts. Record the deployment SHA/time, box-agent version, first request that actually carries `model_instructions` (fresh/restarted/refreshed box or verified context update), and collection window. Old running boxes do not gain new harness code from an engine deploy alone. Exclude turns/requests already in flight before the verified prompt boundary. Keep each session's first applicable prompt timestamp as its own lower bound, rather than treating deployment time alone as proof of the new prompt cohort.

Then copy new excerpts into a distinct folder, retain hashes, and run, once per session's verified boundary:

```sh
node next/scripts/spacing-count.mjs --allow-truncated-first-line --model gpt-6.1-sol \
  --after '<verified first new-prompt request UTC>' --before '<collection end UTC>' \
  '<fresh-session-excerpt.jsonl>' > '<fresh-report.json>'
```

Compare category counts and decoded character denominators with the baseline, state task/context/code-density differences, and explicitly retain the false-positive/false-negative caveat. A human language-quality assessment needs separately authorized inspection, not a claim based on injected test strings or these boundary counts. **After-sample and improvement remain not measured yet.** Build-lead/root supplies the deployment boundary and wakes this builder for the subsequent several-hour collection and remeasurement (goal g206 follow-up); this PR can be reviewed before that live evidence exists.
