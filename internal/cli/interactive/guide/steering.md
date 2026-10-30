# Dyna steering

## Delivery

`dyna post` stores the message and its wake intent. The engine then follows the recipient's state:

- Idle: resumes the exact conversation with the full post, up to 16 KiB.
- Queued: adds the full post to the prompt or live steering queue.
- Running: delivers at a tool boundary. Posts at turn end start a continuation.

Each stored post reaches the session once. A repeated wake creates no empty turn.
A harness that cannot resume the exact conversation rejects follow-ups.
Live frames have a 2000-byte limit. Longer posts include a prefix and pointer:

```text
[Truncated: N of M bytes. Full text: dyna room read <room> <id>]
```

Read the full post once before acting on missing text.
The owner can cancel a waiting steering post. It remains in history but was not delivered.
Apply new instructions while preserving compatible work.

Claude stream-JSON receives messages through stdin at tool boundaries.
Pi receives them as steering on its retained RPC session.
Undelivered messages become continuations.
Other harnesses interrupt and resume immediately when no tool runs, or at the next tool-call completion.
Known tool calls finish before steering interrupts the turn.
Plain-text output has no tool boundaries; `steering.maxToolWait` defaults to 120 seconds before interruption.

```bash
dyna settings set steering.maxToolWait 5m
dyna profiles check
dyna profiles check <name> --live
```

Live checks may incur provider charges and have a two-minute limit.
Harness background tasks end with their process. Run commands directly in your session. Use subagents for durable delegated work.

A profile's explicit `code_mode` setting takes priority. Some models force code mode on.

## Frames and replies

```text
[dyna message msg_ab12 from dyna-agent:decoder]
To: @siblings = dyna-agent:crc, dyna-agent:parser (you)
Reply: dyna post @dyna-agent:decoder "…"   (to all: add @siblings)

The length check also affects parser.c.
```

A reply goes only to the sender. Add the group mention to reply to all.
Scripts cannot receive posts; replies target their parent or owner. Shell completions have no reply command.

```text
Room: dyna-agent   You: dyna-agent:parser
Selectors: @parent = dyna-agent:lead · @siblings = dyna-agent:crc (running) · @children = none
History: dyna room log dyna-agent (read once if your task depends on earlier decisions; never poll)
```

Selectors include open idle sessions. Long groups point to `dyna room members <room>`.
Read `dyna guide posting` for mention rules.

## Reports and turn boundaries

The final message is the turn's full result report.
Post only when a session must act before your final result: a blocker or a decision. A post to @root, or to your parent root, wakes root. Send status to the session that needs to act.
If a successful turn already posted to its report recipient, Dyna sends no duplicate completion. Failures still report.

A session turn reports once on its first turn, with a schema, or after a parent or owner post that names the session.
Naming means its name, `<room>:<name>` address or session ID, or a consolidated session's old name.
A group mention alone (`@team`, `@children`, `@siblings`) creates no report; one beside the session's name still does.
A child's successful report goes only to its current available parent. A top-level session reports to the owner.
If the parent is unavailable, the report goes to the owner. Normal stops report only to the owner.
Actionable failures alert the current stored parent and owner, including provider, quota, rate-limit, schema, and crash failures.
Missing or unavailable parents fall back to the owner.
Ordinary cancellation stays quiet; concrete stop errors alert.
Failed and stopped turns always report. Successful turns started only by child or sibling posts, or only by group mentions such as `@team`, end silently.
Worker posts to `@root`, including depth-1 `@parent`, wake root. Other recipients still wake. Run and shell completions reach their parent and owner; foreground workflow joins return to the caller.
Use `@parent` during a turn when a blocker or handoff needs action.

Session progress shows in the session transcript and in room posts.
Use `dyna subagent show <session>` for the transcript.
Post for a question, blocker, decision, handoff, or ready work that needs action.
Keep routine progress in the transcript and the full result in the final message.
State what is done, what remains, and the next action; link the full result.
Skip routine progress, duplicate reports, and acknowledgements that need no action.
Answer action requests promptly; if busy, act at the next safe boundary without a separate acknowledgement.
Let siblings resolve shared interfaces; send the lead their outcome.
Record a verdict, PR, issue, or file in its artifact.
Encourage useful debate: give evidence, impact, and an alternative. Record decisions; reopen them only with new evidence.
Send the complete final result once, in the final response.
Keep working while a useful next step is available. End your turn when no useful work remains, including while an answer is pending.
Continue available work while awaiting a response. Never poll. Read `dyna guide delegation` for questions and finishing.
