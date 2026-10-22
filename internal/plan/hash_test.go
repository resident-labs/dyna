package plan_test

import (
	"dyna-agent/internal/plan"
	"dyna-agent/internal/planclient"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanHashAndNodeIDsAreStable(t *testing.T) {
	src := `export const meta = { name: 'stable', phases: [{title:'A'}] }
phase('A')
await agent('one', { label: 'one', id: 'a1' })
await parallel([() => agent('two', { label: 'two' })])
return 1
`
	opts := plan.Options{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	a := plan.Extract(src, opts)
	b := plan.Extract(src, plan.Options{Now: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)})
	if a.PlanHash == "" || a.PlanHash != b.PlanHash {
		t.Fatalf("hash %s vs %s", a.PlanHash, b.PlanHash)
	}
	if a.SourceSHA256 != plan.SourceSHA256(src) {
		t.Fatalf("source hash mismatch")
	}
	if len(a.Nodes) != len(b.Nodes) {
		t.Fatalf("node count %d vs %d", len(a.Nodes), len(b.Nodes))
	}
	for i := range a.Nodes {
		if a.Nodes[i].PlanNodeID != b.Nodes[i].PlanNodeID {
			t.Fatalf("node %d id %s vs %s", i, a.Nodes[i].PlanNodeID, b.Nodes[i].PlanNodeID)
		}
	}
}

func TestStaleSourceHashIsDiagnosedNotMerged(t *testing.T) {
	testAncestryEngine(t)
	dir := t.TempDir()
	src := `await agent('old', { label: 'old' }); return 1`
	p := plan.Extract(src, plan.Options{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err := plan.WriteAtomic(dir, p, nil); err != nil {
		t.Fatal(err)
	}
	got, err := plan.Ensure(dir, `await agent('new', { label: 'new' }); return 1`, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	stale := false
	for _, d := range got.Diagnostics {
		if d.Code == "stale_source" {
			stale = true
		}
	}
	if !stale {
		t.Fatalf("stale not diagnosed: %#v", got.Diagnostics)
	}
	if got.PlanHash != p.PlanHash {
		t.Fatal("stale plan was replaced")
	}
	if plan.MatchesSource(p, `await agent('new'); return 1`) {
		t.Fatal("plan.MatchesSource accepted a different script")
	}
}

func TestEnsureWritesMissingAndKeepsMatching(t *testing.T) {
	testAncestryEngine(t)
	dir := t.TempDir()
	src := `await agent('x', { label: 'x' }); return 1`
	first, err := plan.Ensure(dir, src, plan.Options{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.json")); err != nil {
		t.Fatal(err)
	}
	second, err := plan.Ensure(dir, src, plan.Options{Now: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash != second.PlanHash || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("matching plan was rewritten: %#v vs %#v", first, second)
	}
}

func TestEnsureStampsResumedFromOnMatchingPlan(t *testing.T) {
	testAncestryEngine(t)
	dir := t.TempDir()
	src := `await agent('x', { label: 'x' }); return 1`
	first, err := plan.Ensure(dir, src, plan.Options{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if first.ResumedFrom != "" {
		t.Fatalf("fresh plan should omit resume ancestry: %#v", first)
	}
	second, err := plan.Ensure(dir, src, plan.Options{
		WriteAncestry: planclient.StampAncestry, ResumedFrom: "wf_root",
		Now: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ResumedFrom != "wf_root" {
		t.Fatalf("matching plan did not stamp resumedFrom: %#v", second)
	}
	if first.PlanHash != second.PlanHash || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("stamping resume ancestry rewrote the plan: %#v vs %#v", first, second)
	}
	onDisk, err := plan.ReadFile(dir)
	if err != nil || onDisk.ResumedFrom != "wf_root" {
		t.Fatalf("on-disk plan missing durable ancestry: %#v %v", onDisk, err)
	}
	kept, err := plan.Ensure(dir, src, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if kept.ResumedFrom != "wf_root" {
		t.Fatalf("empty opts wiped resume ancestry: %#v", kept)
	}
	reused, err := plan.Ensure(dir, src, plan.Options{
		WriteAncestry: planclient.StampAncestry, ResumedFrom: "wf_other",
		Now: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused.ResumedFrom != "wf_root" {
		t.Fatalf("reused run id overwrote resume ancestry: %#v", reused)
	}
	onDisk, err = plan.ReadFile(dir)
	if err != nil || onDisk.ResumedFrom != "wf_root" {
		t.Fatalf("on-disk lineage lost after reuse: %#v %v", onDisk, err)
	}
}

func TestLookupNestedWorkflowProjectsParentIDSpace(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.js")
	childSrc := `return await pipeline([1], value => agent('nested', { label: 'child-agent' }))`
	if err := os.WriteFile(child, []byte(childSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := plan.Extract(`return await workflow("./child.js")`, plan.Options{
		SourcePath: filepath.Join(dir, "root.js"), WorkDir: dir,
		Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	wfID, ok := plan.LookupNestedWorkflow(parent, child)
	if !ok {
		t.Fatalf("lookup failed: %#v", parent.Workflows)
	}
	var want string
	for _, n := range parent.Nodes {
		if n.Kind == plan.KindAgent && n.Label == "child-agent" {
			want = n.PlanNodeID
		}
	}
	if want == "" {
		t.Fatal("missing inlined child agent")
	}
	projected := plan.ProjectWorkflow(parent, wfID, childSrc, plan.Options{SourcePath: child})
	out, err := plan.InstrumentWorkflow(childSrc, parent, wfID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `__planAgent("`+want+`"`) {
		t.Fatalf("child instrument used a different id space:\n%s\nwant %s projected=%#v", out, want, projected.Nodes)
	}
	if len(projected.StageCallbacks) != 1 || !strings.Contains(out, "__dynaCtx") {
		t.Fatalf("child pipeline callback was not projected and instrumented: callbacks=%#v\n%s", projected.StageCallbacks, out)
	}
}
