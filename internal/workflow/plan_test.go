package workflow

import (
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/plan"
	"dyna-agent/internal/runstore"
)

func TestExecuteEmitsPlanNodeIDsAndSurvivesUnavailableScript(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-plan")
	src := `export const meta = { name: 'p', phases: [{title:'A'}] }
phase('A')
const found = await parallel([() => agent('RESPOND: 1', {label:'one'})])
return found
`
	run, err := runstore.Create("engine-plan", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.ReadFile(run.Dir)
	if err != nil || p.Counts.Agents < 1 {
		t.Fatalf("plan = %#v %v", p, err)
	}
	var sawNode, sawGroup bool
	for _, e := range rec.snapshot() {
		if e.T == "agent_start" && e.PlanNodeID == "" {
			t.Fatalf("instrumented agent_start missing planNodeId: %#v", e)
		}
		if e.T == "agent_start" && e.PlanNodeID != "" {
			sawNode = true
		}
		if e.T == "group_start" {
			sawGroup = true
		}
	}
	if !sawNode {
		t.Fatal("no planNodeId on agent_start")
	}
	if !sawGroup {
		t.Fatal("parallel did not emit group_start")
	}
}

func TestExecuteUnavailablePlanStillRuns(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-unavail")
	src := "return 42"
	run, err := runstore.Create("unavail", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Write a parse-failed plan; Execute must still return.
	if err := plan.WriteAtomic(run.Dir, plan.Unavailable(src, plan.Options{}, "forced"), nil); err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	if result != "42" {
		t.Fatalf("result = %s", result)
	}
}

func TestCachedAgentEndKeepsPlanNodeID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dir := t.TempDir()
	src := `return await agent('RESPOND: {"ok":true}', {label:'cached', schema:{type:'object'}});`
	t.Setenv("DYNA_RUN_ID", "wf_engine-plan-first")
	first, err := runstore.Create("first", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: first, WorkDir: dir,
	})
	first.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(first.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_RUN_ID", "wf_engine-plan-resume")
	second, err := runstore.Create("resume", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err = executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: second, WorkDir: dir, Cache: NewCache(entries), OnEvent: rec.append,
	})
	second.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range rec.snapshot() {
		if e.T == "agent_end" && e.Cached {
			if e.PlanNodeID == "" {
				t.Fatalf("cached agent_end lost planNodeId: %#v", e)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no cached agent_end: %#v", rec.snapshot())
	}
}

func TestNestedChildGetsOwnPlanFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dir := t.TempDir()
	registry := dir + "/registry"
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent('RESPOND: 1', {label:'child'});`)
	root := `return await workflow("child", {});`
	t.Setenv("DYNA_RUN_ID", "wf_engine-child-plan")
	run, err := runstore.Create("parent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, root, Options{
		Store: mockWorkflowStore(0, 0), Run: run, WorkDir: dir, WorkflowDir: registry,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.ReadFile(run.Dir + "/workflows/nested-1"); err != nil {
		t.Fatal(err)
	}
}

func TestNestedChildEmitsParentPlanIDs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dir := t.TempDir()
	registry := dir + "/registry"
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent('RESPOND: 1', {label:'child'});`)
	root := `return await workflow("child", {});`
	parent := plan.Extract(root, plan.Options{WorkDir: dir, WorkflowDir: registry, SourcePath: dir + "/root.js"})
	var wantNode, wantWF string
	for _, n := range parent.Nodes {
		if n.Kind == plan.KindAgent && n.Label == "child" {
			wantNode, wantWF = n.PlanNodeID, n.WorkflowPlanID
		}
	}
	if wantNode == "" || wantWF == "" || wantWF == plan.RootWorkflowID {
		t.Fatalf("parent inlined child missing: %#v", parent.Nodes)
	}
	t.Setenv("DYNA_RUN_ID", "wf_engine-child-ids")
	run, err := runstore.Create("parent-ids", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteAtomic(run.Dir, parent, nil); err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, root, Options{
		Store: mockWorkflowStore(0, 0), Run: run, WorkDir: dir, WorkflowDir: registry, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	var start runstore.Event
	for _, e := range rec.snapshot() {
		if e.T == "agent_start" && e.Label == "child" {
			start = e
		}
	}
	if start.PlanNodeID != wantNode || start.WorkflowPlanID != wantWF {
		t.Fatalf("child event ids = %s/%s want %s/%s events=%#v", start.PlanNodeID, start.WorkflowPlanID, wantNode, wantWF, rec.snapshot())
	}
	joined := plan.Reconcile(parent, []plan.EventView{{
		T: "agent_start", ID: start.ID, Label: start.Label,
		PlanNodeID: start.PlanNodeID, WorkflowPlanID: start.WorkflowPlanID, Workflow: start.Workflow,
	}, {
		T: "agent_end", ID: start.ID, Status: "ok",
		PlanNodeID: start.PlanNodeID, WorkflowPlanID: start.WorkflowPlanID, Workflow: start.Workflow,
	}}, true)
	for _, e := range joined.Entries {
		if e.State == plan.StateUnmatched || (e.PlanNodeID == wantNode && e.State == plan.StateUnobserved) {
			t.Fatalf("id-space split: %#v", joined.Entries)
		}
	}
}

func TestPipelineConcurrentItemsKeepStageItem(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-ctx")
	src := `return await pipeline(['a','b'], async (x) => { await sleep(5); return agent('RESPOND: '+x); })`
	run, err := runstore.Create("pipe-ctx", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[int]bool{"agent_start": {}, "agent_end": {}}
	for _, e := range rec.snapshot() {
		byItem, ok := seen[e.T]
		if !ok {
			continue
		}
		if e.ItemIndex == nil || e.StageIndex == nil {
			t.Fatalf("missing stage/item on %#v", e)
		}
		if *e.StageIndex != 0 {
			t.Fatalf("stage = %d on %#v", *e.StageIndex, e)
		}
		if byItem[*e.ItemIndex] {
			t.Fatalf("duplicate %s itemIndex %d: %#v", e.T, *e.ItemIndex, rec.snapshot())
		}
		byItem[*e.ItemIndex] = true
	}
	for eventType, byItem := range seen {
		if !byItem[0] || !byItem[1] {
			t.Fatalf("%s item indexes = %#v events=%#v", eventType, byItem, rec.snapshot())
		}
	}
}

func TestPipelineStartsItemsConcurrently(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-concurrent")
	src := `return await pipeline(['a','b'], value => agent('RESPOND: '+value, {label: value}))`
	run, err := runstore.Create("pipe-concurrent", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	startsBeforeEnd := 0
	for _, event := range rec.snapshot() {
		if event.T == "agent_end" {
			break
		}
		if event.T == "agent_start" {
			startsBeforeEnd++
		}
	}
	if startsBeforeEnd != 2 {
		t.Fatalf("agent starts before first end = %d, want 2; events=%#v", startsBeforeEnd, rec.snapshot())
	}
}

func TestPipelineHasNoCrossItemStageBarrier(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-no-barrier")
	// Hold the slow item until the fast item reaches its second stage. This
	// proves stage progress without depending on timer order under load.
	src := `let releaseSlow
const fastStage1 = new Promise(resolve => { releaseSlow = resolve })
return await pipeline(['slow','fast'],
  async value => { if (value === 'slow') await fastStage1; log('stage0:'+value); return value },
  value => { log('stage1:'+value); if (value === 'fast') releaseSlow(); return value },
)`
	run, err := runstore.Create("pipe-no-barrier", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	fastStage1, slowStage0 := -1, -1
	for i, event := range rec.snapshot() {
		if event.T != "log" {
			continue
		}
		switch event.Msg {
		case "stage1:fast":
			fastStage1 = i
		case "stage0:slow":
			slowStage0 = i
		}
	}
	if fastStage1 < 0 || slowStage0 < 0 || fastStage1 >= slowStage0 {
		t.Fatalf("pipeline retained a cross-item stage barrier: %#v", rec.snapshot())
	}
}

func TestPipelineNestedParallelAgentsKeepItemContext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-parallel-ctx")
	src := `return await pipeline(['a','b'], async value => {
  await sleep(5)
  return parallel([
    async () => { await sleep(value === 'a' ? 8 : 1); return agent('RESPOND: '+value, {label: value+':one'}) },
    async () => { await sleep(value === 'a' ? 1 : 8); return agent('RESPOND: '+value, {label: value+':two'}) },
  ])
})`
	run, err := runstore.Create("pipe-parallel-ctx", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	wantItem := map[string]int{"a:one": 0, "a:two": 0, "b:one": 1, "b:two": 1}
	seen := make(map[string]bool, len(wantItem)*2)
	for _, event := range rec.snapshot() {
		if event.T != "agent_start" && event.T != "agent_end" {
			continue
		}
		want, ok := wantItem[event.Label]
		if !ok {
			t.Fatalf("unexpected agent label %q: %#v", event.Label, rec.snapshot())
		}
		if event.StageIndex == nil || *event.StageIndex != 0 || event.ItemIndex == nil || *event.ItemIndex != want {
			t.Fatalf("wrong stage/item for %q: %#v", event.Label, event)
		}
		seen[event.T+":"+event.Label] = true
	}
	if len(seen) != len(wantItem)*2 {
		t.Fatalf("seen agents = %#v, want %#v; events=%#v", seen, wantItem, rec.snapshot())
	}
}

func TestPipelineStageCallbackArgumentsStayPublic(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-public-args")
	src := `const original = {marker: 'original'}
return await Promise.all([
  pipeline([original], function zero() { return [zero.length, arguments.length, arguments[0] === original, arguments[1] === original, arguments[2]] }),
  pipeline([original], function one(value) { return [one.length, arguments.length, arguments[0] === original, arguments[1] === original, arguments[2]] }),
  pipeline([original], function two(value, item) { return [two.length, arguments.length, arguments[0] === original, arguments[1] === original, arguments[2]] }),
  pipeline([original], function three(value, item, index) { return [three.length, arguments.length, arguments[0] === original, arguments[1] === original, arguments[2]] }),
])`
	run, err := runstore.Create("pipe-public-args", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	want := `[[[0,3,true,true,0]],[[1,3,true,true,0]],[[2,3,true,true,0]],[[3,3,true,true,0]]]`
	if result != want {
		t.Fatalf("result = %s, want %s", result, want)
	}
}

func TestPipelineRestStageCallbackKeepsPublicArgsAndContext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-rest-args")
	src := `return await pipeline(['original'], async (...values) => {
  await sleep(1)
  await agent('RESPOND: rest', {label: 'rest'})
  return [values.length, values[0], values[1], values[2]]
})`
	run, err := runstore.Create("pipe-rest-args", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	if result != `[[3,"original","original",0]]` {
		t.Fatalf("result = %s", result)
	}
	seen := map[string]bool{}
	for _, event := range rec.snapshot() {
		if event.T != "agent_start" && event.T != "agent_end" {
			continue
		}
		if event.PlanNodeID == "" || event.StageIndex == nil || *event.StageIndex != 0 || event.ItemIndex == nil || *event.ItemIndex != 0 {
			t.Fatalf("rest callback agent lost lexical context: %#v", event)
		}
		seen[event.T] = true
	}
	if !seen["agent_start"] || !seen["agent_end"] {
		t.Fatalf("missing rest callback agent events: %#v", rec.snapshot())
	}
}

func TestPipelineResolvedStageValueExpressionsKeepLexicalContext(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "array index",
			src: `return await pipeline(['item'], [async value => {
  await Promise.resolve()
  return agent('SLEEP:0\nRESPOND: array', {label: 'array'})
}][0])`,
		},
		{
			name: "object property",
			src: `return await pipeline(['item'], ({callback: async function (value) {
  await Promise.resolve()
  return agent('SLEEP:0\nRESPOND: object', {label: 'object'})
}}).callback)`,
		},
		{
			name: "inline factory with nested parallel",
			src: `return await pipeline(['item'], (() => async value => {
  await Promise.resolve()
  return parallel([async () => {
    await Promise.resolve()
    return agent('SLEEP:0\nRESPOND: iife', {label: 'iife'})
  }])
})())`,
		},
		{
			name: "awaited async inline factory",
			src: `return await pipeline(['item'], await (async () => async value => {
  await Promise.resolve()
  return agent('SLEEP:0\nRESPOND: async factory', {label: 'async-factory'})
})())`,
		},
		{
			name: "simple block inline factory",
			src: `return await pipeline(['item'], (() => {
  const harmless = 1
  return async value => {
    await Promise.resolve(harmless)
    return agent('SLEEP:0\nRESPOND: block factory', {label: 'block-factory'})
  }
})())`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv(runstore.AgentJournalRootEnv, "")
			t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-resolved-"+strings.ReplaceAll(tt.name, " ", "-"))
			run, err := runstore.Create("pipe-resolved", tt.src, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := &workflowEventRecorder{}
			result, err := executeWorkflowTest(t, tt.src, Options{
				Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
			})
			run.Finish("ok", result, err)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, event := range rec.snapshot() {
				if event.T != "agent_start" && event.T != "agent_end" {
					continue
				}
				if event.PlanNodeID == "" || event.PlanGroupID == "" || event.StageIndex == nil || *event.StageIndex != 0 ||
					event.ItemIndex == nil || *event.ItemIndex != 0 {
					t.Fatalf("resolved callback agent lost lexical context: %#v", event)
				}
				seen[event.T] = true
			}
			if !seen["agent_start"] || !seen["agent_end"] {
				t.Fatalf("missing agent events: %#v", rec.snapshot())
			}
		})
	}
}

func TestPipelineEscapedIdentifiersKeepLexicalContextAndSemantics(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "escaped context collision",
			src: `const \u005f_dynaCtx1 = 'SLEEP:0\nRESPOND: preserved'
return await pipeline(['item'], async value => {
  await Promise.resolve()
  return agent(\u005f_dynaCtx1, {label: 'escaped-binding'})
})`,
			want: `["preserved"]`,
		},
		{
			name: "escaped named function",
			src: `return await pipeline(['item'], async function \u0073tage(value) {
  await Promise.resolve()
  await agent('SLEEP:0\nRESPOND: named', {label: 'escaped-name'})
  return \u0073tage.name
})`,
			want: `["stage"]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv(runstore.AgentJournalRootEnv, "")
			t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-escaped-"+strings.ReplaceAll(tt.name, " ", "-"))
			run, err := runstore.Create("pipe-escaped", tt.src, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := &workflowEventRecorder{}
			result, err := executeWorkflowTest(t, tt.src, Options{
				Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
			})
			run.Finish("ok", result, err)
			if err != nil {
				t.Fatal(err)
			}
			if result != tt.want {
				t.Fatalf("result = %s, want %s", result, tt.want)
			}
			seen := map[string]bool{}
			for _, event := range rec.snapshot() {
				if event.T != "agent_start" && event.T != "agent_end" {
					continue
				}
				if event.PlanNodeID == "" || event.PlanGroupID == "" || event.StageIndex == nil || *event.StageIndex != 0 ||
					event.ItemIndex == nil || *event.ItemIndex != 0 {
					t.Fatalf("escaped identifier agent lost lexical context: %#v", event)
				}
				seen[event.T] = true
			}
			if !seen["agent_start"] || !seen["agent_end"] {
				t.Fatalf("missing escaped identifier agent events: %#v", rec.snapshot())
			}
		})
	}
}

func TestPipelineAgentCallCommentSeparatorKeepsLexicalContext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-call-comment")
	src := `return await pipeline(['item'], async value => {
  await sleep(1)
  return agent /* legal separator */ ('RESPOND: comment', {label: 'comment-separator'})
})`
	run, err := runstore.Create("pipe-call-comment", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range rec.snapshot() {
		if event.T != "agent_start" && event.T != "agent_end" {
			continue
		}
		if event.PlanNodeID == "" || event.PlanGroupID == "" || event.StageIndex == nil || *event.StageIndex != 0 ||
			event.ItemIndex == nil || *event.ItemIndex != 0 {
			t.Fatalf("comment-separated call lost lexical context: %#v", event)
		}
		seen[event.T] = true
	}
	if !seen["agent_start"] || !seen["agent_end"] {
		t.Fatalf("missing agent events: %#v", rec.snapshot())
	}
}

func TestPipelineNamedFunctionStageKeepsStableSelfIdentity(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-stage-identity")
	src := `const seen = []
const result = await pipeline(['a', 'b'], async function stage(value) {
  await sleep(value === 'a' ? 2 : 1)
  seen.push(stage)
  stage.calls = (stage.calls || 0) + 1
  return [stage.name, stage.length, stage.calls]
})
return {same: seen[0] === seen[1], calls: seen[0].calls, result}`
	run, err := runstore.Create("pipe-stage-identity", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"same":true,"calls":2,"result":[["stage",1,2],["stage",1,1]]}` &&
		result != `{"same":true,"calls":2,"result":[["stage",1,1],["stage",1,2]]}` {
		t.Fatalf("named stage did not retain stable self identity/state: %s", result)
	}
}

func TestPipelineNamedFunctionStageRecursionKeepsLexicalContext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-stage-recursion")
	src := `let captured
const result = await pipeline([2, 3], async function stage(value, original) {
  await sleep(value % 2 + 1)
  captured = stage
  stage.calls = (stage.calls || 0) + 1
  if (value > 0) return await stage(value - 1, original)
  return agent('RESPOND: recursive-' + original, {label: 'recursive-' + original})
})
return {name: captured.name, calls: captured.calls, result}`
	run, err := runstore.Create("pipe-stage-recursion", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"name":"stage","calls":7,"result":["recursive-2","recursive-3"]}` {
		t.Fatalf("recursive stage result = %s", result)
	}
	wantItem := map[string]int{"recursive-2": 0, "recursive-3": 1}
	seen := map[string]map[string]bool{"agent_start": {}, "agent_end": {}}
	for _, event := range rec.snapshot() {
		byLabel, ok := seen[event.T]
		if !ok {
			continue
		}
		item, ok := wantItem[event.Label]
		if !ok || event.PlanNodeID == "" || event.PlanGroupID == "" ||
			event.StageIndex == nil || *event.StageIndex != 0 ||
			event.ItemIndex == nil || *event.ItemIndex != item {
			t.Fatalf("recursive stage agent lost lexical context: %#v; events=%#v", event, rec.snapshot())
		}
		byLabel[event.Label] = true
	}
	for eventType, labels := range seen {
		if len(labels) != len(wantItem) {
			t.Fatalf("%s labels = %#v; events=%#v", eventType, labels, rec.snapshot())
		}
	}
}

func TestPipelineInstrumentationHelperNameCollisions(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "top level",
			src: `const __planAgent = () => 'wrong agent'
const __planPipeline = () => ['wrong pipeline']
const __planStage = () => 7
return await pipeline(['item'], async value => {
  await Promise.resolve()
  return agent('SLEEP:0\nRESPOND: top-level', {label: 'top-level'})
})`,
			want: `["top-level"]`,
		},
		{
			name: "enclosing function",
			src: `async function run() {
  const __planAgent = () => 'wrong agent'
  const __planPipeline = () => ['wrong pipeline']
  const __planStage = () => 7
  return pipeline(['item'], async value => {
    await Promise.resolve()
    return agent('SLEEP:0\nRESPOND: enclosing', {label: 'enclosing'})
  })
}
return await run()`,
			want: `["enclosing"]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv(runstore.AgentJournalRootEnv, "")
			t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-helper-collision-"+strings.ReplaceAll(tt.name, " ", "-"))
			run, err := runstore.Create("pipe-helper-collision", tt.src, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := &workflowEventRecorder{}
			result, err := executeWorkflowTest(t, tt.src, Options{
				Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
			})
			run.Finish("ok", result, err)
			if err != nil {
				t.Fatal(err)
			}
			if result != tt.want {
				t.Fatalf("helper collision result = %s, want %s", result, tt.want)
			}
			seen := map[string]bool{}
			for _, event := range rec.snapshot() {
				if event.T != "agent_start" && event.T != "agent_end" {
					continue
				}
				if event.PlanNodeID == "" || event.PlanGroupID == "" ||
					event.StageIndex == nil || *event.StageIndex != 0 ||
					event.ItemIndex == nil || *event.ItemIndex != 0 {
					t.Fatalf("helper collision lost lexical context: %#v", event)
				}
				seen[event.T] = true
			}
			if !seen["agent_start"] || !seen["agent_end"] {
				t.Fatalf("helper collision suppressed agent events: result=%s events=%#v", result, rec.snapshot())
			}
		})
	}
}

func TestPlannedPipelineDropsOnlyFailingItem(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_engine-pipe-drop")
	src := `return await pipeline([1, 2], value => {
  if (value === 1) throw new Error('drop')
  return value + 1
}, value => value * 2)`
	run, err := runstore.Create("pipe-drop", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &workflowEventRecorder{}
	result, err := executeWorkflowTest(t, src, Options{
		Store: mockWorkflowStore(0, 0), Run: run, OnEvent: rec.append,
	})
	run.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	if result != `[null,6]` {
		t.Fatalf("result = %s, want [null,6]", result)
	}
	found := false
	for _, event := range rec.snapshot() {
		if event.T == "log" && event.Provenance == runstore.LogProvenanceSystem && strings.HasPrefix(event.Msg, "pipeline: item 0 dropped: drop") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing pipeline drop system log: %#v", rec.snapshot())
	}
}
