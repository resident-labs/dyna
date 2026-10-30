package subagent

import (
	"dyna-agent/internal/guidance"
	"regexp"
	"strings"
	"testing"
)

func TestAgentsUseSupportedStateSnapshots(t *testing.T) {
	for role, meta := range map[string]Meta{
		"worker": {},
		"root":   {Role: RoleRoot},
		"leaf":   {Leaf: true},
	} {
		rules := rulesFor(meta) + workerContext(meta, "", 3)
		for _, want := range []string{"Never open or copy the live dyna.db directly.", "use the latest daily backup first", "dyna state snapshot <path>", "only when you need newer state", "at most one snapshot at a time", "Snapshots contain private state"} {
			if !strings.Contains(rules, want) {
				t.Errorf("%s instructions missing %q", role, want)
			}
		}
	}
}

func TestInstructionsTeachDynaOnlyDelegation(t *testing.T) {
	meta := Meta{
		ID: "ses_20260101-120000-abcdef12", Name: "decoder",
		RoomID: "room_20260101-120000-abcdef12", Room: "amber-otter-42", Depth: 2,
	}
	got := sessionRules(true, false) + "\n\n" + workerContext(meta, "amber-otter-42:lead", 3)
	for _, want := range []string{
		"You are amber-otter-42:decoder", "Room: amber-otter-42   You: amber-otter-42:decoder",
		"`dyna post @<mention>... \"...\"`", "From: amber-otter-42:lead",
		"dyna room members amber-otter-42", "Use the inherited room and its existing watches", "Across turns, set", "dyna goal done <id>",
		"Put temporary files in $DYNA_SCRATCH.",
		"Edit code only in dyna git worktrees new trees. Never edit the owner’s main checkout.",
		"Delegate only through Dyna", "rather than harness subagents, Task/Agent or workflow tools",
		`"$DYNA_BIN" subagent start --name <unique-name> --profile <profile>`,
		"omit --room", "Continue work while children run", "dyna request question", "goes dormant by itself", "Leave merging to the Dyna merge service",
		"git`: before you create a worktree, open, track, review or comment on a pull request",
		"dyna git pr comment", clearEnglishPrompt, parentCheckInPrompt,
		"Turns started only by child, sibling or group posts end silently.",
		"address the session that asked.",
		`If dyna is absent from PATH, use "$DYNA_BIN" for all commands, including guides.`,
		"You are at depth 2 of 3.",
		"Only root uses dyna request question; ask @parent otherwise, and post @root when only the owner can unblock you.",
		"Success goes to the current parent",
		"A post to @root, or to your parent root, wakes root.",
		"Give a full, self-contained final result.",
		"A recorded verdict, a tracked PR, and a finished goal need no extra post.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
	for _, gone := range []string{"dyna msg", "inbox", "Inbox", "Explore:", "Peers ", "subscribe", "phrase", "journal", "DYNA_AGENT_JOURNAL", "A successful report goes to the owner without waking you"} {
		if strings.Contains(got, gone) {
			t.Errorf("instructions still say %q", gone)
		}
	}
	if !strings.Contains(workerContext(Meta{Room: "r", Name: "n", Depth: 3}, "", 3), "start no subagent sessions") {
		t.Fatal("depth limit instruction was lost")
	}
}

// The prompt carries the task and live context; the standing instructions
// travel separately, so they are not in it.
func TestTurnPromptsCarryTaskAndLiveContextOnly(t *testing.T) {
	roomContext := "Room: amber-otter-42   You: amber-otter-42:decoder\nSelectors: @parent = amber-otter-42:lead"
	first := firstTurnPrompt(roomContext, "Audit it")
	if !strings.HasPrefix(first, "[ORIGINAL TASK]\nAudit it") || !strings.HasSuffix(first, roomContext+"\n[/DYNA WORKER CONTEXT]") || strings.Contains(first, clearEnglishPrompt) {
		t.Errorf("first-turn prompt = %q", first)
	}
	if !strings.Contains(firstTurnPrompt(roomContext, ""), "in your Dyna instructions") {
		t.Error("a brief-only first turn does not point at the instructions")
	}
	follow := followUpPrompt(2, "next", roomContext)
	if !strings.Contains(follow, "turn 2") || !strings.Contains(follow, roomContext) || strings.Contains(follow, sessionRules(true, false)) || !strings.HasSuffix(follow, "\n\nnext") {
		t.Errorf("follow-up prompt = %q", follow)
	}
	withFormat := withSchema(first, `{"type":"object"}`)
	if strings.Index(withFormat, "OUTPUT FORMAT:") <= strings.Index(withFormat, "Audit it") {
		t.Fatal("schema instructions must follow the task")
	}
}

func TestSessionCoreBudgetAndGuideTriggers(t *testing.T) {
	core := strings.ReplaceAll(sessionRules(true, false), clearEnglishPrompt, "")
	// Machine discovery added a required topic; keep the core lean.
	if words := len(strings.Fields(core)); words >= 580 {
		t.Fatalf("session core is %d words; must stay under 580", words)
	}
	topics := regexp.MustCompile("(?m)^- `([a-z-]+)`: ").FindAllStringSubmatch(core, -1)
	var want []string
	for _, topic := range guidance.Topics {
		if topic.Name != guidance.RootTopic {
			want = append(want, topic.Name)
		}
	}
	if len(topics) != len(want) {
		t.Fatalf("core lists %d topics, want every session topic %v", len(topics), want)
	}
	for i, topic := range topics {
		if topic[1] != want[i] {
			t.Errorf("core topic %d is %q, want %q", i, topic[1], want[i])
		}
	}
	if strings.Contains(core, guidance.RootTopic) {
		t.Fatal("session core must not direct sessions to external orchestration")
	}
}

// Compare only each role's shared session rules. Room rules, profile data,
// worker identity, service contracts, and the goal/brief are separate sections.
func TestSessionRuleSizesStayComparable(t *testing.T) {
	if !strings.Contains(rootRules(), parentCheckInPrompt) {
		t.Fatal("root instructions omit parent check-in responsibility")
	}
	root, worker := len(rootRules()), len(sessionRules(true, false))
	t.Logf("root session part: %d bytes; worker session part: %d bytes", root, worker)
	shorter, longer := root, worker
	if shorter > longer {
		shorter, longer = longer, shorter
	}
	if longer*100 > shorter*125 {
		t.Fatalf("session parts differ by more than 25 percent: root=%d, worker=%d", root, worker)
	}
}

// Root and workers both get the post-on-change rule, so room rule r11 can go.
func TestBothRolesPostOnlyOnChange(t *testing.T) {
	for role, rules := range map[string]string{"worker": rulesFor(Meta{}), "root": rulesFor(Meta{Role: RoleRoot})} {
		for _, want := range []string{"Post only to the session that must act next", "Address @root only when root must act", "Your final result is your report."} {
			if !strings.Contains(rules, want) {
				t.Errorf("%s rules lack %q", role, want)
			}
		}
	}
}
