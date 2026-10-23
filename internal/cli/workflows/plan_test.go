package workflows

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/plan"
	"dyna-agent/internal/runstore"
)

func TestRunPlanPrintsVersionedJSONAndStartsNoWorker(t *testing.T) {
	inRepo(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	script := filepath.Join(dir, "wf.js")
	if err := os.WriteFile(script, []byte(`export const meta = { name: 'p', phases: [{title:'A'}] }
phase('A')
await agent('hello', { label: 'hi' })
return 1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewRunCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{script, "--plan", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var p plan.Plan
	if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
		t.Fatalf("plan json: %v\n%s", err, stdout.String())
	}
	if p.Version != 1 || p.ExtractorVersion != plan.ExtractorVersion || p.PlanHash == "" {
		t.Fatalf("envelope = %#v", p)
	}
	if p.Counts.Agents != 1 {
		t.Fatalf("agents = %d nodes=%#v", p.Counts.Agents, p.Nodes)
	}
	if entries, err := runstore.List(); err != nil || len(entries) != 0 {
		t.Fatalf("run --plan created a run: %#v %v", entries, err)
	}
}

func TestRunPlanUnavailableOnInvalidScript(t *testing.T) {
	inRepo(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "bad.js")
	if err := os.WriteFile(script, []byte("function ("), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewRunCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{script, "--plan"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var p plan.Plan
	if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusUnavailable {
		t.Fatalf("status = %s", p.Status)
	}
}

func TestRunsShowJSONKeepsEventsResultAndAddsPlan(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveWorkflowEngine(t)
	t.Setenv("DYNA_RUN_ID", "wf_show-plan")
	run, err := runstore.Create("show-plan", "await agent('x', {label:'x'}); return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.Extract("await agent('x', {label:'x'}); return 1", plan.Options{})
	if err := plan.WriteAtomic(run.Dir, p, nil); err != nil {
		t.Fatal(err)
	}
	run.Append(runstore.Event{T: "run_start", Title: "show-plan"})
	run.Append(runstore.Event{T: "agent_start", ID: 1, Label: "x", PlanNodeID: agentsFirst(p)})
	run.Append(runstore.Event{T: "agent_end", ID: 1, Label: "x", Status: "ok", PlanNodeID: agentsFirst(p)})
	run.Finish("ok", "1", nil)

	cmd := NewRunsCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"show", run.Meta.ID, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"events", "result", "plan", "reconciliation", "mermaid"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing %s in %s", key, stdout.String())
		}
	}
	events, _ := payload["events"].([]any)
	if len(events) < 2 {
		t.Fatalf("events = %#v", payload["events"])
	}
}

func TestRunsGraphPrintsMermaid(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveWorkflowEngine(t)
	t.Setenv("DYNA_RUN_ID", "wf_graph")
	run, err := runstore.Create("graph", "await agent('x', {label:'x'}); return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.Extract("await agent('x', {label:'x'}); return 1", plan.Options{})
	if err := plan.WriteAtomic(run.Dir, p, nil); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "1", nil)
	cmd := NewRunsCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"graph", run.Meta.ID})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "flowchart TD") || !strings.Contains(stdout.String(), "legend") {
		t.Fatalf("graph = %s", stdout.String())
	}
}

func TestAttachedCreateWritesPlanBeforeRunStart(t *testing.T) {
	inRepo(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveWorkflowEngine(t)
	t.Setenv(runstore.AgentJournalRootEnv, "")
	mockProfile(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "wf.js")
	if err := os.WriteFile(script, []byte(`return await agent("hi", {label:"hi"})`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewRunCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{script, "--quiet", "--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	runs, err := runstore.List()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	id := runs[0].ID
	p, err := plan.ReadFile(filepath.Join(runstore.RunsDir(), id))
	if err != nil {
		t.Fatal(err)
	}
	if p.Counts.Agents < 1 {
		t.Fatalf("prelaunch plan has no agents: %#v", p)
	}
	events, err := runstore.ReadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	var start int
	for i, e := range events {
		if e.T == "run_start" {
			start = i
		}
		if e.T == "agent_start" && i < start {
			t.Fatal("agent_start before run_start")
		}
		if e.T == "agent_start" && e.PlanNodeID == "" {
			t.Fatalf("instrumented agent_start missing planNodeId: %#v", e)
		}
	}
}

func agentsFirst(p *plan.Plan) string {
	for _, n := range p.Nodes {
		if n.Kind == plan.KindAgent {
			return n.PlanNodeID
		}
	}
	return ""
}
