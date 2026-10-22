package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func extractTest(src string, opts Options) *Plan {
	if opts.Now.IsZero() {
		opts.Now = time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	}
	return Extract(src, opts)
}

func agentsOf(p *Plan) []Node {
	var out []Node
	for _, n := range p.Nodes {
		if n.Kind == KindAgent {
			out = append(out, n)
		}
	}
	return out
}

func kindsOf(p *Plan) []string {
	var out []string
	for _, n := range p.Nodes {
		out = append(out, n.Kind)
	}
	return out
}

func TestExtractSequentialAgentsAndPhases(t *testing.T) {
	src := `export const meta = { name: 'seq', phases: [{title:'Fix'},{title:'Check'}] }
phase('Fix')
const a = await agent('do fix', { profile: 'builder', label: 'fix', id: 'fix-1' })
phase('Check')
const b = await agent('review it', { profile: 'reviewer', label: 'review', phase: 'Check' })
return { a, b }
`
	p := extractTest(src, Options{})
	if p.Status != StatusComplete {
		t.Fatalf("status = %s diags=%v", p.Status, p.Diagnostics)
	}
	if len(p.Phases) != 2 || p.Phases[0].Title != "Fix" || p.Phases[1].Title != "Check" {
		t.Fatalf("declared phases = %#v", p.Phases)
	}
	agents := agentsOf(p)
	if len(agents) != 2 {
		t.Fatalf("agents = %#v kinds=%v", agents, kindsOf(p))
	}
	if agents[0].Label != "fix" || agents[0].Profile != "builder" || agents[0].Phase != "Fix" || agents[0].AuthorID != "fix-1" {
		t.Fatalf("first agent = %#v", agents[0])
	}
	if agents[0].Certainty != CertaintyCertain || agents[0].Cardinality.Kind != CardExact {
		t.Fatalf("first certainty/card = %s %#v", agents[0].Certainty, agents[0].Cardinality)
	}
	if agents[1].Label != "review" || agents[1].Phase != "Check" {
		t.Fatalf("second agent = %#v", agents[1])
	}
	if !strings.Contains(agents[0].PromptPreview, "do fix") {
		t.Fatalf("preview = %q", agents[0].PromptPreview)
	}
}

func TestExtractDirectParallel(t *testing.T) {
	src := `
const [a, b] = await parallel([
  () => agent('one', { label: 'a' }),
  () => agent('two', { label: 'b' }),
])
return [a, b]
`
	p := extractTest(src, Options{})
	var par *Node
	for i := range p.Nodes {
		if p.Nodes[i].Kind == KindParallel {
			par = &p.Nodes[i]
			break
		}
	}
	if par == nil {
		t.Fatalf("missing parallel node: %v", kindsOf(p))
	}
	if par.Cardinality.Kind != CardExact || par.Cardinality.Max == nil || *par.Cardinality.Max != 2 {
		t.Fatalf("parallel card = %#v", par.Cardinality)
	}
	agents := agentsOf(p)
	if len(agents) != 2 {
		t.Fatalf("agents = %#v", agents)
	}
	for _, a := range agents {
		if a.GroupID == "" {
			t.Fatalf("agent missing group: %#v", a)
		}
	}
}

func TestExtractPipelineLiteralAndDynamic(t *testing.T) {
	lit := `return await pipeline(['a','b'], (x) => agent('s1 '+x, {label:'s1'}), (x) => agent('s2 '+x, {label:'s2'}))`
	p := extractTest(lit, Options{})
	var pipe *Node
	for i := range p.Nodes {
		if p.Nodes[i].Kind == KindPipeline {
			pipe = &p.Nodes[i]
		}
	}
	if pipe == nil || pipe.Cardinality.Kind != CardExact || pipe.Cardinality.Max == nil || *pipe.Cardinality.Max != 2 {
		t.Fatalf("literal pipeline = %#v kinds=%v", pipe, kindsOf(p))
	}
	agents := agentsOf(p)
	if len(agents) != 2 {
		t.Fatalf("pipeline agents = %#v", agents)
	}
	for _, a := range agents {
		if a.Certainty != CertaintyTemplate {
			t.Fatalf("stage agent certainty = %s", a.Certainty)
		}
	}

	dyn := `return await pipeline(items, (x) => agent(x))`
	d := extractTest(dyn, Options{})
	for _, n := range d.Nodes {
		if n.Kind == KindPipeline && n.Cardinality.Kind != CardUnknown {
			t.Fatalf("dynamic pipeline card = %#v", n.Cardinality)
		}
	}
}

func TestExtractBranchesAndLoops(t *testing.T) {
	src := `
if (args.ok) {
  await agent('yes', { label: 'yes' })
} else {
  await agent('no', { label: 'no' })
}
for (const x of items) {
  await agent(x, { label: 'looped' })
}
return true
`
	p := extractTest(src, Options{})
	var branch, loop bool
	for _, n := range p.Nodes {
		if n.Kind == KindBranch {
			branch = true
		}
		if n.Kind == KindLoop {
			loop = true
		}
	}
	if !branch || !loop {
		t.Fatalf("kinds = %v", kindsOf(p))
	}
	var yes, looped *Node
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.Kind == KindAgent && n.Label == "yes" {
			yes = n
		}
		if n.Kind == KindAgent && n.Label == "looped" {
			looped = n
		}
	}
	if yes == nil || yes.Certainty != CertaintyConditional {
		t.Fatalf("yes = %#v", yes)
	}
	if looped == nil || looped.Certainty != CertaintyTemplate || looped.Cardinality.Kind != CardUnknown {
		t.Fatalf("looped = %#v", looped)
	}
}

func TestExtractComputedValues(t *testing.T) {
	src := `
const name = args.p
await agent(name, { profile: name, label: name, phase: name })
return true
`
	p := extractTest(src, Options{})
	agents := agentsOf(p)
	if len(agents) != 1 {
		t.Fatalf("agents = %#v", agents)
	}
	if agents[0].PromptPreview != "${…}" {
		t.Fatalf("preview = %q", agents[0].PromptPreview)
	}
	if agents[0].Profile != "" || agents[0].Label != "agent" {
		t.Fatalf("computed fields leaked literals: %#v", agents[0])
	}
}

func TestExtractTraversesRecognizedHookArgumentsInScopeOrder(t *testing.T) {
	src := `
await agent(agent('inner agent', {label: 'inner-agent'}), {label: 'outer-agent'})
log(agent('inner log', {label: 'inner-log'}))
phase(agent('inner phase', {label: 'inner-phase'}))
workflow(agent('inner ref', {label: 'inner-ref'}), agent('inner args', {label: 'inner-args'}))
function shadowed(agent) { log(agent('not global')) }
return true
`
	p := extractTest(src, Options{})
	agents := agentsOf(p)
	want := []string{"inner-agent", "outer-agent", "inner-log", "inner-phase", "inner-ref", "inner-args"}
	if len(agents) != len(want) {
		t.Fatalf("agents = %#v, want labels %v", agents, want)
	}
	for i, label := range want {
		if agents[i].Label != label {
			t.Fatalf("agent %d label = %q, want %q; agents=%#v", i, agents[i].Label, label, agents)
		}
	}
}

func TestExtractLiteralAndDynamicNestedWorkflow(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.js")
	if err := os.WriteFile(child, []byte(`return await agent('nested', { label: 'child-agent' })`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `return await workflow("./child.js")`
	p := extractTest(src, Options{SourcePath: filepath.Join(dir, "root.js"), WorkDir: dir})
	var childAgent bool
	for _, n := range p.Nodes {
		if n.Kind == KindAgent && n.Label == "child-agent" && n.WorkflowPlanID != RootWorkflowID {
			childAgent = true
		}
	}
	if !childAgent {
		t.Fatalf("literal child agent missing: nodes=%#v workflows=%#v", p.Nodes, p.Workflows)
	}

	dyn := extractTest(`return await workflow(args.ref)`, Options{WorkDir: dir})
	found := false
	for _, n := range dyn.Nodes {
		if n.Kind == KindWorkflow && n.PlanNodeID != RootWorkflowID && n.Certainty == CertaintyUnknown {
			found = true
		}
	}
	if !found {
		t.Fatalf("dynamic workflow not unknown: %#v", dyn.Nodes)
	}
}

func TestExtractShadowedAPIsAreNotPlanned(t *testing.T) {
	src := `
function run(agent) {
  return agent('shadowed')
}
const pipeline = () => {}
pipeline([1], x => agent('real', { label: 'real' }))
return run(() => 'no')
`
	p := extractTest(src, Options{})
	agents := agentsOf(p)
	if len(agents) != 1 || agents[0].Label != "real" {
		t.Fatalf("shadowed extract = %#v kinds=%v", agents, kindsOf(p))
	}
}

func TestExtractInvalidSyntaxUnavailable(t *testing.T) {
	p := extractTest("function (", Options{})
	if p.Status != StatusUnavailable || len(p.Diagnostics) == 0 {
		t.Fatalf("invalid = %#v", p)
	}
}

func TestExtractTopLevelExportReturnAwait(t *testing.T) {
	src := `export const meta = { name: 'top' }
const x = await agent('hello', { label: 'hi' })
return x
`
	p := extractTest(src, Options{})
	if p.Status == StatusUnavailable {
		t.Fatalf("top-level form unavailable: %v", p.Diagnostics)
	}
	if len(agentsOf(p)) != 1 {
		t.Fatalf("top-level agents = %#v diags=%v", p.Nodes, p.Diagnostics)
	}
}

func TestExtractStartsNoHarness(t *testing.T) {
	p := extractTest(`await agent('x'); return 1`, Options{})
	if p.Counts.Agents != 1 {
		t.Fatalf("counts = %#v", p.Counts)
	}
}

func TestExtractDuplicateAuthorIDsDiagnosed(t *testing.T) {
	src := `
await agent('a', { id: 'same', label: 'a' })
await agent('b', { id: 'same', label: 'b' })
return 1
`
	p := extractTest(src, Options{})
	found := false
	for _, d := range p.Diagnostics {
		if d.Code == "duplicate_author_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate id not diagnosed: %#v", p.Diagnostics)
	}
	if len(agentsOf(p)) != 2 {
		t.Fatalf("nodes merged: %#v", agentsOf(p))
	}
}

func TestExtractMapCallbackIsTemplate(t *testing.T) {
	src := `return await parallel(['auth','billing','frontend'].map(area => () => agent('scan '+area, { label: area })))`
	p := extractTest(src, Options{})
	agents := agentsOf(p)
	if len(agents) != 1 {
		t.Fatalf("map should share one template node, got %#v", agents)
	}
	if agents[0].Certainty != CertaintyTemplate {
		t.Fatalf("map agent certainty = %s", agents[0].Certainty)
	}
	if agents[0].Cardinality.Kind != CardExact || agents[0].Cardinality.Max == nil || *agents[0].Cardinality.Max != 3 {
		t.Fatalf("map card = %#v", agents[0].Cardinality)
	}
}

func TestExtractEmptyBranchRestoresSequence(t *testing.T) {
	src := `
await agent('a', { label: 'a' })
if (args.skip) { foo() }
await agent('b', { label: 'b' })
return 1
`
	p := extractTest(src, Options{})
	var aID, bID string
	for _, n := range p.Nodes {
		if n.Kind == KindAgent && n.Label == "a" {
			aID = n.PlanNodeID
		}
		if n.Kind == KindAgent && n.Label == "b" {
			bID = n.PlanNodeID
		}
	}
	if aID == "" || bID == "" {
		t.Fatalf("missing agents: %#v", p.Nodes)
	}
	for _, n := range p.Nodes {
		if n.Kind == KindBranch {
			t.Fatalf("empty if should have been dropped: %#v", n)
		}
	}
	found := false
	for _, e := range p.Edges {
		if e.Kind == EdgeSequence && e.From == aID && e.To == bID {
			found = true
		}
		if e.Kind == EdgeSequence && (e.From != aID && e.From != RootWorkflowID) && e.To == bID {
			t.Fatalf("dangling sequence into b: %#v nodes=%#v", e, p.Nodes)
		}
	}
	if !found {
		t.Fatalf("missing a→b sequence: edges=%#v", p.Edges)
	}
}

func TestExtractLogIsKindLogNotUnknown(t *testing.T) {
	p := extractTest("log('hi'); await agent('x', {label:'x'}); return 1", Options{})
	var logs, unknowns int
	for _, n := range p.Nodes {
		if n.Kind == KindLog {
			logs++
		}
		if n.Kind == KindUnknown && n.Label == "log" {
			unknowns++
		}
	}
	if logs != 1 || unknowns != 0 {
		t.Fatalf("log nodes: logs=%d unknowns=%d kinds=%v", logs, unknowns, kindsOf(p))
	}
}
