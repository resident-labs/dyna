package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCIBudgetIgnoresGitHubReportingStalls(t *testing.T) {
	for name, steps := range map[string]string{
		"finished steps, unfinished job": `{"name":"Test","status":"completed","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:00:03Z"}`,
		"job accepted, no steps":         "",
		"gap between steps":              `{"name":"Setup","status":"completed","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:00:02Z"},{"name":"Test","status":"completed","started_at":"2026-01-01T00:09:00Z","completed_at":"2026-01-01T00:09:03Z"}`,
		"pending step with timestamp":    `{"name":"Test","status":"pending","started_at":"2026-01-01T00:00:00Z"}`,
		"finished step without end":      `{"name":"Test","status":"completed","started_at":"2026-01-01T00:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
			budgetFixture(e, e.clock, "pull_request")
			e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = fmt.Sprintf("HTTP/2.0 200 OK\r\n\r\n"+`{"jobs":[{"name":"Go tests","status":"in_progress","started_at":"2026-01-01T00:00:00Z","steps":[%s]}]}`, steps)
			e.track()
			e.tick()
			if r := e.record(); r.CI == "failed" || r.CIBudgetRuns["81/1"] != "" || len(r.CIBudgetNotes) != 0 {
				t.Fatalf("reporting stall counted as execution: CI=%s, budget=%v", r.CI, r.CIBudgetRuns)
			}
			calls, _ := os.ReadFile(e.calls)
			if strings.Contains(string(calls), "/cancel") {
				t.Fatal("cancelled a reporting stall")
			}
		})
	}
}

func TestCIBudgetClearsSavedJobTimeFailure(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	budgetFixture(e, e.clock, "pull_request")
	e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n" + `{"jobs":[{"name":"Go tests","status":"in_progress","started_at":"2026-01-01T00:00:00Z","steps":[{"name":"Test","status":"completed","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:00:03Z"}]}]}`
	e.track()
	r := e.record()
	// The old critical-path verdict still used job wall time. Reading that saved
	// schema must not make its failure permanent under runner-step timing.
	if err := json.Unmarshal([]byte(`{"ciBudgetCritical":{"81/1":true}}`), &r); err != nil {
		t.Fatal(err)
	}
	r.CIBudgetHead = r.Head
	r.CIBudgetRuns = map[string]string{"81/1": "over CI time budget (cancel failed)"}
	r.CIBudgetNotes = map[string]string{"81/1": "old warning"}
	text := "CI for " + r.URL + " exceeded its budget; cancellation failed: over CI time budget (cancel failed)."
	notice(&r, r.Author, text, "ci-budget:"+r.Head+":81/1:over CI time budget (cancel failed)")
	if err := e.s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r = e.record()
	if r.CI == "failed" || r.CIBudgetRuns["81/1"] != "within budget" || !r.CIBudgetSteps["81/1"] || len(r.CIBudgetNotes) != 0 {
		t.Fatalf("old job verdict survived: CI=%s, budget=%v, notes=%v", r.CI, r.CIBudgetRuns, r.CIBudgetNotes)
	}
	for _, n := range e.sent {
		if strings.Contains(n.Text, "over CI time budget") {
			t.Fatal("sent superseded budget failure")
		}
	}
}
