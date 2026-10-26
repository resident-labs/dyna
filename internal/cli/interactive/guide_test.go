package interactive

import (
	"dyna-agent/internal/authority"
	"dyna-agent/internal/guidance"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/store/storetest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func topicDocument(t *testing.T, name string) string {
	t.Helper()
	doc, err := guideDocument(name)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestGuideDocumentsRuntimeContractAndRunnableExamples(t *testing.T) {
	guideMD := topicDocument(t, "workflows")
	required := []string{
		"## Public JavaScript API",
		"### `agent(prompt, opts?)`", "### `parallel(thunks)`",
		"### `pipeline(items, ...stages)`", "### `post(to, text)`",
		"### `workflow(nameOrRef, args?)`",
		"post('@root',", "### Workflow templates", "dyna templates copy",
		"dyna run /tmp/my-review.js", "adversarial-review", "judge-panel", "research-design",
		"meta` is a convention, not a validated runtime schema",
		"three attempts total", "no stage barrier",
		"(previousResult, originalItem, index)", "Handle `null` results",
		"## Workflow shape: parallel by default", "Name the work list before writing the script",
		"engine-verified path from `worktrees()`", "never trust worker-reported paths",
		"Parallel writers need disjoint scopes or worktrees",
		"A child must not call `workflow()`", "share parent concurrency and lifetime limits",
		"regular agent session that cannot start subagents or runs",
		"maxCallsPerRun", "--max-concurrent", "--max-agents",
		"`phase(title)`", "`log(message)`", "`sleep(ms)`", "`args`", "`process`", "`profiles`", "`worktrees()`",
		`provenance: "workflow"`, `provenance: "system"`,
	}
	if words := len(strings.Fields(guideMD)); words > 1390 {
		t.Fatalf("workflow guide is %d words; want at most 1390", words)
	}
	for _, contract := range required {
		if !strings.Contains(guideMD, contract) {
			t.Errorf("guide is missing contract or example %q", contract)
		}
	}
}

func TestGuideExcludesUnsupportedWorkflowConcepts(t *testing.T) {
	guideMD := topicDocument(t, "workflows")
	// "/workflows" and "workflow(name" are deliberately not forbidden here:
	// dyna's own workflow(nameOrRef, args) primitive and its default registry
	// path (.../dyna/workflows) are now legitimately documented. "Date.now"/
	// "Math.random" are also allowed: the resume non-determinism warning
	// legitimately names them as the APIs that break --resume cache hits.
	for _, term := range []string{
		"ultracode", "<task-notification>", "StructuredOutput", "budget.remaining",
		"agentType", "opts.effort", "saved workflow", "nested workflow",
		"4096 items",
	} {
		if strings.Contains(strings.ToLower(guideMD), strings.ToLower(term)) {
			t.Errorf("guide contains unsupported workflow concept %q", term)
		}
	}
}

func TestReadmeDocumentsDesktopAndPublicCommands(t *testing.T) {
	b, err := os.ReadFile("../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{
		"desktop/build.sh", "`/<profile> <task>`", "member list", "dyna engine run", "dyna guide",
		"dyna post", "dyna room watch", "dyna room log", "dyna room read", "dyna room members",
		"Mentions decide who wakes, not who can read", "not a security",
		"dyna root attach", "dyna subagent start", "dyna run", "dyna ask", "Authors\nnever merge",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("README is missing %q", want)
		}
	}
	for _, old := range []string{
		"inbox", "phrase", "dyna msg", "msg(", "subscribe", "dyna room show", "dyna room tree",
		"--inbox", "dyna_inbox", "peers", "peer snapshot", "email address", "shared stream",
		"direct message", "- **Subagents**", "five views", "journal",
		"dyna tui", "four views", "terminal dashboard",
	} {
		if strings.Contains(strings.ToLower(doc), strings.ToLower(old)) {
			t.Errorf("README contains retired surface %q", old)
		}
	}
}

func TestGuideTopicContracts(t *testing.T) {
	contracts := map[string][]string{
		"git":              {"worktree-created", "before-land", "after-land", "pr-merged", "main-updated", "session-finish", "schedule", "dyna git hooks list", "dyna git hooks log", "dyna git hooks add", "dyna git hooks edit", "dyna git hooks test", "dyna git hooks run", "dyna git hooks rm", "dyna post @parent", "one-off", "generic repo event", "outside the repo", "can change files or services", "dyna git pr track", "dyna git pr verdict", "--head <assigned-sha>", "dyna git pr admit", "dyna git pr review", "dyna git pr comment", "Never add HTML markers or agent attribution", "Agent reviews stay in Dyna", "cannot publish GitHub PR comments or reviews", "dyna git pr resume", "dyna git pr noise", "agents never run merge commands", "dyna git change open", "dyna git change reply", "--findings-file", "dyna git pr budget", "10 seconds", "No stamp means root", "no acting `--session` flag", "outside commenter", "CI and land notices never start an agent", "then the current repo", "Reviewers never run the test suite", "in parallel with CI", "steer the live reviewer", "answers each comment", "dyna git change policy", "dyna git pr merge-status", "No stamp means root", "exact-head approval", "--reason", "automatically", "oldest-ready", "retargets children"},
		"delegation":       {"dyna subagent start", "Manage sessions with `dyna session`", "dyna subagent output", "--schema", "subagent.maxDepth", "DYNA_MODE", "DYNA_SESSION", "DYNA_DEPTH", "DYNA_ROOM", "DYNA_ADDRESS", "--use agents,services", "no timeout, no idle expiry, no turn cap", "never through harness subagents or workflows", "Workflow agents are leaf sessions", "hands PR authorship and held goals to its nearest open ancestor", "Reviewer roles return to the pool", "A failed handoff keeps the session open", "Stored hierarchy controls parent selectors and depth", "validates the first turn as JSON", "--goal \"<outcome; done when>\"", "Context:", "Owned files:", "Checks:", "Return: At most 1200 characters", "goes dormant by itself", "dyna goal", "dyna ask", "parent-checkins", "Dormant and closed children do not count", "Dyna closes a dormant session after 7 dormant days", "| `DYNA_AGENT_JOURNAL` | not set | journal path |"},
		"posting":          {"A room has no private messages", "Mentions decide who wakes, not who can read", "not a security boundary", "Records without `roomId` fail closed", "mention := @<local> | @<room>:<local> | @<room> | @run:<id>/agent:<n>", "| Sender | `@team` | `@parent` | `@siblings` | `@children` |", "unstamped external shell must qualify every mention", "Mixed rooms are rejected", "The sender is removed from groups", "Resolution errors store nothing", "non-resumable explicit names", "skipped[]", "{to, reason}", "{message, deliveries, skipped?}", "{to, delivery, turn?, pointer?, error?}", "session-steered", "session-queued", "session-followup", "CLI exits nonzero", "successful deliveries remain", "reaches the open leaf session of that workflow agent", "including depth-1 `@parent`, wake root", "--html", "--attach", "dyna attachment get", "dyna room pin", "dyna room list", "dyna room log <room>", "--all", "--tail N", "--json", "dyna room read <room> <id>", "dyna room members <room>", "dyna room search <words>", "--sessions", "the last 14 days"},
		"steering":         {"[Truncated: N of M bytes. Full text: dyna room read <room> <id>]", "[dyna message msg_ab12", "To: @siblings =", "Reply: dyna post @", "Selectors: @parent =", "A reply goes only to the sender", "next tool-call completion", "steering.maxToolWait", "with a schema", "names the session", "A group mention alone", "only by group mentions such as `@team`, end silently", "Failed and stopped turns always report", "Keep working while a useful next step is available", "End your turn when no useful work remains", "cancel a waiting steering post", "Encourage useful debate", "Known tool calls are never killed for steering"},
		"computers":        {"dyna computer open", "-- foot", "waits for a new window", "returned log path", "dyna computer env", "dyna computer status", "dyna computer shot", "dyna computer act", "No stamp means root", "root-<room id>", "create it on first use", "no acting `--session` flag", "secret:true", "Use `dyna browser` for web work", "dyna browser open", "chromium.connectOverCDP", "newCDPSession(page)", "Performance.getMetrics", "dyna browser stop", "same Unix account", "`status` and `stop` do not", "`status` takes no session argument"},
		"recovery":         {"dyna subagent transfer", "dyna subagent history", "dyna subagent consolidate", "30-second", "--token <requested-token>", "dyna room pause", "dyna room resume", "dyna room clear-dormant", "backups/pre-migration/", "no idle timer ever makes a session dormant", "429", "same room", "source worktrees", "without asking approval", "Live PR guards still apply", "independent reviewer"},
		guidance.RootTopic: {"one root seat", "dyna root attach --room <room>", "dyna root detach --room <room>", "dyna root status --room <room>", "wait in the room log", "one JSON block", "Attaching twice changes nothing", "`--handoff <file>`", "`--handoff -`", "next turn input", "waits up to 30 seconds", "transcript fallback", "then stops its turn", "never reach the internal root"},
	}
	for name, wants := range contracts {
		doc := topicDocument(t, name)
		for _, want := range wants {
			if !strings.Contains(doc, want) {
				t.Errorf("%s misses %q", name, want)
			}
		}
	}
}

func guideOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewGuideCommand()
	cmd.SilenceUsage = true
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs(append([]string{"--plain"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestGuideIndexAndTopicRoutes(t *testing.T) {
	files, err := guideFiles.ReadDir("guide")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(guidance.Topics) {
		t.Fatalf("%d files for %d topics", len(files), len(guidance.Topics))
	}
	for _, stamp := range []string{"", mode.Subagent, mode.Root} {
		t.Setenv("DYNA_MODE", stamp)
		t.Setenv("DYNA_SESSION", "ses_test")
		block := guidance.RootBlock(stamp == mode.Root)
		external := stamp == ""
		index, err := guideOutput(t)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(index, block) != (stamp != mode.Subagent) {
			t.Errorf("stamp %q: root block shown=%v", stamp, stamp == mode.Subagent)
		}
		if strings.Contains(index, guidance.RootTopic+": ") != external {
			t.Errorf("stamp %q: %s listed=%v", stamp, guidance.RootTopic, !external)
		}
		want := len(guidance.Topics)
		if !external {
			want--
		}
		triggers := strings.TrimSuffix(strings.TrimPrefix(index, block), "\n"+authority.Guide())
		if lines := strings.Count(triggers, "\n"); lines != want {
			t.Errorf("stamp %q: index has %d trigger lines, want %d", stamp, lines, want)
		}
		for _, topic := range guidance.Topics {
			if external && strings.Count(index, topic.Name+": ") != 1 {
				t.Errorf("missing index trigger for %s", topic.Name)
			}
			out, err := guideOutput(t, topic.Name)
			if err != nil || out != topicDocument(t, topic.Name) {
				t.Errorf("stamp %q: wrong output for topic %q: %v", stamp, topic.Name, err)
			}
		}
	}
	for internal, wants := range map[bool][]string{
		false: {"**You are root,**", "Do not pass these decisions to the owner", "ask it with `dyna ask`", `dyna room create --repo "$PWD"`, "`dyna root attach --room <room> --harness <name>`", "`dyna root detach --room <room>`", "`--claude`", "`--other`"},
		true:  {"**You are root,**", "Do not pass these decisions to the owner", "ask it with `dyna ask`", "internal root", "Never run `dyna room watch`", "`dyna rules list`", "`dyna rules list --global`", "`dyna root attach`", "`dyna root detach`"},
	} {
		for _, want := range wants {
			if !strings.Contains(guidance.RootBlock(internal), want) {
				t.Errorf("root block (internal=%v) misses %q", internal, want)
			}
		}
	}
	if strings.Contains(guidance.RootBlock(true), "dyna room create") {
		t.Error("the internal root block tells it to create a room")
	}
	for _, name := range []string{"quick", "subagents", "subagent", "workflow", "nope"} {
		cmd := NewGuideCommand()
		cmd.SilenceUsage = true
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		cmd.SetArgs([]string{name})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown guide topic") {
			t.Errorf("accepted %q: %v", name, err)
		}
	}
}

func TestExternalOrchestrationPrintsOneHarnessRecipe(t *testing.T) {
	storetest.ClearStamp(t)
	recipes := map[string]string{"claude": "Monitor tool with the command", "other": "Print one complete JSON line and exit"}
	list, err := guideOutput(t, guidance.RootTopic)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root-addressed alerts", "issue", "cleanup notices", "No second --all stream", "quiet posts stay held", "one JSON block", "last waking post ID", "timed-flush notice", "--lines", "-- since <post-id>", "dyna room read <room> <post-id>"} {
		if !strings.Contains(list, want) {
			t.Errorf("shared root guide misses %q", want)
		}
	}
	for name, marker := range recipes {
		if !strings.Contains(list, "--"+name) || strings.Contains(list, marker) {
			t.Errorf("flag list must name --%s and hold no recipe", name)
		}
		out, err := guideOutput(t, guidance.RootTopic, "--"+name)
		if err != nil {
			t.Fatal(err)
		}
		for other, otherMarker := range recipes {
			if strings.Contains(out, otherMarker) != (other == name) {
				t.Errorf("--%s output and %s recipe disagree", name, other)
			}
		}
		for _, want := range []string{"dyna room watch", "--since <post-id>", `dyna rules list --room "$room"`, "dyna rules list --global", "dyna guide posting", "root-addressed alerts", "issue", "cleanup notices", "No second --all stream", "quiet posts stay held", "one JSON block", "last waking post ID", "timed-flush notice", "--lines", "-- since <post-id>", "dyna room read \"$room\" <post-id>"} {
			if !strings.Contains(out, want) {
				t.Errorf("--%s misses %q", name, want)
			}
		}
	}
	if _, err := guideOutput(t, "git", "--claude"); err == nil {
		t.Error("harness flag accepted on another topic")
	}
	if _, err := guideOutput(t, guidance.RootTopic, "--claude", "--other"); err == nil {
		t.Error("two harness flags accepted")
	}
}

func TestGuideLinksAndSharedContracts(t *testing.T) {
	refs := regexp.MustCompile("dyna guide[ \n]+([a-z-]+)")
	counts := map[string]int{}
	for _, topic := range guidance.Topics {
		doc := topicDocument(t, topic.Name)
		for _, match := range refs.FindAllStringSubmatch(doc, -1) {
			if strings.HasPrefix(match[1], "--") || match[1] == "is" {
				continue
			}
			if _, err := guideDocument(match[1]); err != nil {
				t.Errorf("%s has broken link: %s", topic.Name, match[0])
			}
		}
		for _, text := range []string{"mention :=", "A session turn reports once", "## Questions", "## Goals", "A profile's explicit `code_mode` setting takes priority.", "Session progress shows in the session transcript and in room posts.", "Each delivery is", "## Room history", "## Finishing"} {
			counts[text] += strings.Count(doc, text)
		}
		for _, term := range []string{"dyna msg", "dyna inbox", "dyna subagent wait", "Every session has a work journal"} {
			if strings.Contains(doc, term) {
				t.Errorf("%s contains retired surface %q", topic.Name, term)
			}
		}
	}
	for text, count := range counts {
		if count != 1 {
			t.Errorf("%q has %d homes; want one", text, count)
		}
	}
}

func TestWorkflowGuideSaysHowToWatchAndSteerAnAgent(t *testing.T) {
	doc := topicDocument(t, "workflows")
	for _, want := range []string{"open its session from its row on the run page", "dyna post @run:<id>/agent:<n>", "shows only the journal and the final result"} {
		if !strings.Contains(doc, want) {
			t.Errorf("workflow guide misses %q", want)
		}
	}
}

func TestWorkflowGuideStartsWithStructuredParallelExample(t *testing.T) {
	doc := topicDocument(t, "workflows")
	first := strings.SplitN(doc, "## Profiles", 2)[0]
	for _, want := range []string{"const schema =", "await parallel([", "() => agent(", "{ schema }", "dyna run check.js", "Without `schema`"} {
		if !strings.Contains(first, want) {
			t.Errorf("workflow opening misses %q", want)
		}
	}
	if words := len(strings.Fields(first)); words > 300 {
		t.Fatalf("workflow opening is %d words", words)
	}
}

func TestComputersGuideIncludesActionSchema(t *testing.T) {
	doc := topicDocument(t, "computers")
	for _, want := range []string{"click: x y button [keys]", "wait: [ms]", "ms: integer from 0 to 10000; default 1000 milliseconds.", "5 waits", "10 screenshots"} {
		if !strings.Contains(doc, want) {
			t.Errorf("computer guide misses %q", want)
		}
	}
}

func TestGitGuideReplacesVersionControlTopics(t *testing.T) {
	posting := topicDocument(t, "posting")
	if !strings.Contains(posting, "For issue filing and triage, read `dyna guide git`.") || strings.Contains(posting, "dyna issue") || strings.Contains(posting, "dyna git issue") {
		t.Error("posting guide must point to the git guide without duplicate issue instructions")
	}
	doc, err := guideDocument("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"dyna git pr", "dyna git pr export", "dyna git change", "dyna git ci", "dyna git worktrees", "dyna git hooks", "dyna issue", "dyna git sync status", "dyna git ci run", "dyna git ci show"} {
		if !strings.Contains(doc, command) {
			t.Errorf("git guide misses %s", command)
		}
	}
	for _, term := range []string{"dyna check", "effective check policy", "Use `dyna check"} {
		if strings.Contains(doc, term) {
			t.Errorf("git guide still mentions retired surface %q", term)
		}
	}
	for _, retired := range []string{"pr", "landing", "checks", "hooks"} {
		if _, err := guideDocument(retired); err == nil {
			t.Errorf("old topic %q still splits the git guide", retired)
		}
	}
}

func TestGitGuideRootMergeControls(t *testing.T) {
	doc := topicDocument(t, "git")
	for _, want := range []string{"dyna git merge status", "dyna git merge pause", "dyna git merge resume", "dyna git merge cancel", "dyna git merge return", "dyna git merge retry", "--without", "dyna git merge bisect", "dyna git merge publish", "one waking notice", "Successful batches", "authors", "on demand"} {
		if !strings.Contains(doc, want) {
			t.Errorf("git guide misses %q", want)
		}
	}
}

// An agent in any repository must find the local CI file and its keys from the
// git guide, not only from the dyna checkout's docs/local-ci.md.
func TestGitGuideDocumentsRepositoryLocalCIDefinition(t *testing.T) {
	doc := topicDocument(t, "git")
	for _, want := range []string{
		"`.dyna/local-ci.yaml`", "`checks`, `suites` and `trial`",
		"`image`", "`caches`", "RE2 `files`", "`host: true`",
		"--allow-host-checks", "docs/local-ci.md",
		"rust:slim", "python:3.13-slim",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("git guide misses local CI definition %q", want)
		}
	}
}

func TestSteeringGuideAsksForNoAcknowledgement(t *testing.T) {
	doc := topicDocument(t, "steering")
	if strings.Contains(doc, "acknowledge at the next safe boundary") {
		t.Fatal("the steering guide still asks busy sessions to acknowledge requests")
	}
}

// A dyna command without DYNA_OWNER_ENV or DYNA_BIN keeps the harness's
// private roots and writes a second store into its runtime home, so no guide
// may tell agents to unset them, alone or as all DYNA_* variables.
func TestNoGuideTellsAgentsToUnsetTheOwnerEnvironment(t *testing.T) {
	unset := regexp.MustCompile("(?i)(\\bunset|-u)\\s+`?DYNA_(\\*|OWNER_ENV\\b|BIN\\b)|DYNA_\\*`?\\s+variables\\s+unset|unset\\s+(all|inherited)\\s+`?DYNA_")
	files, err := guideFiles.ReadDir("guide")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := guideFiles.ReadFile("guide/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(raw), "\n") {
			if unset.MatchString(line) {
				t.Errorf("guide/%s:%d tells agents to unset the owner environment: %s", file.Name(), n+1, line)
			}
		}
	}
}

func TestExitOnlyHarnessUsesJSONDelivery(t *testing.T) {
	storetest.ClearStamp(t)
	out, err := guideOutput(t, guidance.RootTopic, "--other")
	if err != nil {
		t.Fatal(err)
	}
	_, fallback, ok := strings.Cut(out, "If the harness wakes you only when a process exits,")
	if !ok {
		t.Fatal("missing exit-only fallback")
	}
	if !strings.Contains(fallback, "`dyna room watch \"$room\"`") || !strings.Contains(fallback, "one complete JSON line") || !strings.Contains(fallback, "block's `id`") {
		t.Fatalf("exit-only fallback must retain all quiet posts, the waking post and its cursor:\n%s", fallback)
	}
}
