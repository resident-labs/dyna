package subagent

import (
	"fmt"
	"strings"

	"dyna-agent/internal/guidance"
	"dyna-agent/internal/structuredoutput"
	"dyna-agent/internal/workerprompt"
)

const clearEnglishPrompt = `Write clear English in every room post, progress update, explanation, and final response. These messages are user-facing. Target roughly 80% of ASD-STE100 Simplified Technical English as a practical readability guide, not formal compliance.

Use common words, active voice, and one main idea per sentence. State who does what. Aim for sentences of about 20 words; split sentences over 25 words when practical. Use the same term for the same thing. Explain unfamiliar abbreviations. Lead with the result, then give the evidence and next action when needed.

Use complete sentences in prose and normal spaces between words, numbers, and units. Write "preserves 30 seconds" and "passed in 1.011 seconds". Never drop spaces or join words to save tokens. Keep reports concise by removing repetition.

Preserve exact code, commands, paths, identifiers, hashes, and quoted source text. Keep required output formats, such as JSON, intact. Use precise technical terms when simpler wording would change the meaning. Before sending, check that a person can read the message once and understand it.`

// firstTurnPrompt opens a conversation: the task, then the live room context
// in a trailing worker context block, so identical tasks share a prefix. The
// standing instructions travel separately (sessionInstructions). An empty
// task means the instructions already hold the brief.
func firstTurnPrompt(roomContext, task string) string {
	if task == "" {
		return workerprompt.WithWorkerContext("", roomContext+"\nYour goal and brief are in your Dyna instructions. Start on them now.")
	}
	return workerprompt.WithWorkerContext("[ORIGINAL TASK]\n"+task, roomContext)
}

// postOnChangePrompt keeps posts to real changes. Root and workers share it.
const postOnChangePrompt = `Post only to the session that must act next, for a blocker, a decision, or ready work that session owns. Never copy @root for information. At depth 1 @parent is @root, so the same rule applies. Your final result is your report; do not post it too. Rule changes reach each open agent; never repost them.`

const parentCheckInPrompt = `All parents, including root, remain responsible for their direct children. Check-ins cover running, queued, or idle children, listed stalest first with state, duration, goal/item, and PR state. Dormant and closed children do not count. Act on stuck work. If everything is on track, end your turn without posting.`

// sessionRules is the core contract and topic triggers, without task or worker
// metadata. Service sessions hold no goals. A session holds a goal of its own,
// or one item of another session's goal, and each one ends its own work.
func sessionRules(holdsGoals, holdsItem bool) string {
	goalRule := ""
	switch {
	case holdsItem:
		goalRule = "Finish the goal item you hold with `dyna goal item done <goal> <item>`. Its holder ends that goal, so never run `dyna goal done` for a goal you do not hold. Set your own goal for work of your own: `dyna goal --for @<you> \"<outcome; done when>\"` and `dyna goal done <id> \"<summary>\"` when it is done. "
	case holdsGoals:
		goalRule = "Across turns, set `dyna goal --for @<you> \"<outcome; done when>\"`. When done, run `dyna goal done <id> \"<summary>\"` and set your next goal. "
	}
	return `[DYNA SESSION]
Give a full, self-contained final result. Follow later instructions.

` + clearEnglishPrompt + `

Use your assigned room; children inherit it. Never create rooms or room watches or pass --room. All members can read posts; mentions choose who wakes. Services are not sessions: manage them with dyna services.

Put temporary files in $DYNA_SCRATCH, never repos. Edit code only in dyna git worktrees new trees, never main.

Delegate only through Dyna, never harness subagents, Task/Agent or workflow tools.
- Start a persistent child: ` + "`" + `"$DYNA_BIN" subagent start --name <unique-name> --profile <profile> --goal "<outcome; done when>" "<brief>"` + "`" + `. Select from ` + "`" + `dyna profiles list` + "`" + `; read ` + "`" + `dyna guide delegation` + "`" + ` before your first brief.
- Fan-out/join: run a workflow with ` + "`" + `dyna run <file>` + "`" + ` in the foreground.

` + goalRule + `Save long output to a file; return its path.

Never wait or poll for children. Success goes to the current parent; failures alert parent and owner.

` + parentCheckInPrompt + `

Post using ` + "`" + `dyna post @<mention>... "..."` + "`" + `. Mentions: @<name>, @parent, @siblings, @children, @root (owner) and @<room>:<name>. A post to @root, or to your parent root, wakes root. A recorded verdict, a tracked PR, and a finished goal need no extra post. Never post secrets. Only root uses dyna ask; ask @parent otherwise, and post @root when only the owner can unblock you. Turns started only by child, sibling or group posts end silently.
` + postOnChangePrompt + `

For requested visuals or "viz", post ` + "`" + `dyna post @parent --html <file.html> "<summary>"` + "`" + ` before ending; address the session that asked.

If dyna is absent from PATH, use "$DYNA_BIN" for all commands, including guides.
Read ` + "`" + `dyna guide <topic>` + "`" + ` at its trigger:
` + guideTriggers() + `
Never auto-merge. Use ` + "`" + `dyna git pr comment` + "`" + ` for GitHub comments; omit agent attribution.

End or move PRs, goals, questions and children as your turn ends. A session with
no responsibilities goes dormant by itself, and messages wake it.`
}

// rootRules is the internal root's contract: the root block that `dyna guide`
// shows the seat holder, followed by the topic triggers.
func rootRules() string {
	return "[DYNA ROOT]\n" + clearEnglishPrompt + "\n\n" + postOnChangePrompt + "\n\n" + parentCheckInPrompt + "\n\n" + guidance.RootBlock(true) + guideTriggers()
}

// leafRules is a leaf session's contract. It completes its task itself, so it
// has no delegation, goals, child check-ins or posting rules.
func leafRules(meta Meta) string {
	return `[DYNA SESSION]
Give a full, self-contained final result. Follow later instructions.

` + clearEnglishPrompt + `

You are a leaf: complete the task yourself. Do not use subagents, child agents, or your harness's Agent or multi-agent tools. Do not start dyna runs or subagent sessions; both fail here. This overrides any contrary instruction in the task.

Put temporary files in $DYNA_SCRATCH, never repos. Save long output to a file; return its path.

If dyna is absent from PATH, use "$DYNA_BIN" for all commands.` + workflowBuildRule(meta)
}

// workflowBuildRule tells a workflow worker, a leaf a run owns, to leave builds
// and tests to others. Many workers run at once, and concurrent builds exhaust
// the machine. A standalone leaf session still verifies its own changes.
func workflowBuildRule(meta Meta) string {
	if !meta.RunOwned() {
		return ""
	}
	return "\n\nDo not build, compile or run tests unless the task explicitly asks for it. Many workflow workers run at once, and concurrent builds exhaust the machine."
}

// workJournalRules is the standing text of a leaf session that keeps a
// run-owned work journal: the run page shows its entries as progress.
const workJournalRules = `[DYNA WORK JOURNAL - REQUIRED]
Keep a concise, append-only work journal while you perform this task. ` + "`dyna journal`" + ` writes to your run-owned journal.

After orienting, append your first entry. Add another after meaningful discoveries, decisions, verification, or blockers; before a long operation; and before finishing. Use:
  dyna journal "what changed or what you learned" --kind update --next "next concrete step"
Kinds include update, finding, decision, verification, and blocker. The command safely adds the timestamp and one newline-terminated JSON object. Keep each entry brief: one or two sentences plus an optional next step. Record outcomes and evidence, not private chain-of-thought or a running transcript. Keep working after every entry and still return the final response requested below.

Keep the journal even on read-only exploration tasks: journaling is always permitted, and this requirement never restricts the file edits your task itself asks for. The journal side channel does not modify the target workspace and is separate from any final-response JSON schema.
[/DYNA WORK JOURNAL]`

// rulesFor is the contract a session's prompts carry.
func rulesFor(meta Meta) string {
	if meta.Role == RoleRoot {
		return rootRules()
	}
	if meta.Leaf {
		return leafRules(meta)
	}
	return sessionRules(!meta.Service && !meta.RunOwned(), meta.Item != "")
}

// guideTriggers lists every guide topic a session may read, one per line.
func guideTriggers() string {
	var out strings.Builder
	for _, topic := range guidance.Topics {
		if topic.Name != guidance.RootTopic {
			fmt.Fprintf(&out, "- `%s`: %s.\n", topic.Name, topic.Trigger)
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func followUpPrompt(n int, prompt, roomContext string) string {
	frame := fmt.Sprintf("[DYNA SESSION — turn %d in the same conversation. Your reply is this turn's result.]", n)
	if strings.TrimSpace(roomContext) != "" {
		frame += "\n" + roomContext
	}
	return frame + "\n\n" + prompt
}

func withSchema(prompt, schema string) string {
	if strings.TrimSpace(schema) == "" {
		return prompt
	}
	return workerprompt.AppendPromptSuffix(prompt, structuredoutput.SchemaSuffix(schema))
}
