package transcript

import (
	"testing"
	"time"

	"dyna-agent/internal/shells"
	"dyna-agent/internal/workeroutput"
)

const (
	job1 = "sh_20261020-094405-3e9e3b0a"
	job2 = "sh_20261020-094406-4f0a1c2b"
)

func toolCall(name, input, output string) workeroutput.Event {
	return workeroutput.Event{V: 1, Kind: "tool", Name: name, State: "completed", Input: input, Output: output}
}

func TestSummarizeLinksDynaShellCallsToTheirJob(t *testing.T) {
	run := `{"command":"go test ./...","description":"Run the tests"}`
	for _, name := range []string{"mcp__dyna__shell", "dyna_shell", "dyna.shell", "dyna/shell"} {
		for _, output := range []string{
			"Job " + job1 + ". Exit code 0.\nok",
			"Job " + job1 + ". Still running after 30s, so it moved to the background.",
		} {
			tool := Summarize(toolCall(name, run, output))
			if tool.JobID != job1 || tool.JobOp != "run" || tool.Description != "Run the tests" || tool.Detail != "go test ./..." {
				t.Errorf("%s %q: tool = %#v", name, output, tool)
			}
		}
	}
	for name, op := range map[string]string{"mcp__dyna__shell_output": "output", "dyna_shell_stop": "stop", "dyna.shell_output": "output"} {
		if tool := Summarize(toolCall(name, `{"job_id":"`+job2+`"}`, "")); tool.JobID != job2 || string(tool.JobOp) != op {
			t.Errorf("%s: tool = %#v", name, tool)
		}
	}
}

func TestSummarizeLeavesOtherCallsUnlinked(t *testing.T) {
	for _, e := range []workeroutput.Event{
		toolCall("mcp__dyna__shell", `{"command":"x"}`, "description is required: one short line that says what the command does"),
		toolCall("mcp__dyna__shell", `{"command":"x","description":"Old"}`, "Exit code 0.\nok"),
		toolCall("mcp__dyna__shell_jobs", `{}`, job1+"  running for 3s  Run"),
		toolCall("mcp__dyna__shell_stop", `{"job_id":"../../etc"}`, ""),
		// A harness's own shell tool has no job; Pi is one of them.
		toolCall("bash", `{"command":"x"}`, "Job "+job1+". Exit code 0."),
		toolCall("shell", `{"command":"x"}`, "Job "+job1+". Exit code 0."),
	} {
		if tool := Summarize(e); tool.JobID != "" {
			t.Errorf("%s linked to %q", e.Name, tool.JobID)
		}
	}
}

func job(id, name, command string, started time.Time) shells.Info {
	return shells.Info{Meta: shells.Meta{ID: id, Name: name, Cmd: []string{"bash", "-c", command}, Started: started}}
}

func TestLinkJobsGuessesOnlyWhatNoResultNames(t *testing.T) {
	at := time.Now()
	call := func(output string) workeroutput.Event {
		return toolCall("mcp__dyna__shell", `{"command":"make","description":"Build"}`, output)
	}
	jobs := []shells.Info{job(job1, "Build", "make", at), job(job2, "Build", "make", at.Add(time.Second))}
	build := func(events ...workeroutput.Event) []Item {
		items, _ := FromEvents(events)
		LinkJobs(items, jobs)
		return items
	}

	// A call that is still running has no result: the oldest free job matches.
	if items := build(call("")); items[0].Tool.JobID != job1 {
		t.Fatalf("guess = %q", items[0].Tool.JobID)
	}
	// The result that follows names job 2, and replaces the guess.
	if items := build(call("Job " + job2 + ". Exit code 0.")); items[0].Tool.JobID != job2 {
		t.Fatalf("named = %q", items[0].Tool.JobID)
	}
	// A guess never takes a job another call's result names.
	items := build(call(""), call("Job "+job1+". Exit code 0."))
	if items[0].Tool.JobID != job2 || items[1].Tool.JobID != job1 {
		t.Fatalf("pairs = %q, %q", items[0].Tool.JobID, items[1].Tool.JobID)
	}
	// Two pending calls pair with jobs in start order, once each.
	items = build(call(""), call(""), call(""))
	if got := items[0].Tool.JobID + "," + items[1].Tool.JobID + "," + items[2].Tool.JobID; got != job1+","+job2+"," {
		t.Fatalf("pairs = %q", got)
	}
	// A different command or description never matches.
	jobs = []shells.Info{job(job1, "Build", "make test", at), job(job2, "Other", "make", at)}
	if items := build(call("")); items[0].Tool.JobID != "" {
		t.Fatalf("mismatch linked to %q", items[0].Tool.JobID)
	}
}

func TestOutputAndStopCallsDoNotClaimTheLaunchOfTheirJob(t *testing.T) {
	at := time.Now()
	jobs := []shells.Info{job(job1, "Build", "make", at), job(job2, "Build", "make", at.Add(time.Second))}
	items, _ := FromEvents([]workeroutput.Event{
		// Two retained runs from before results named their job.
		toolCall("mcp__dyna__shell", `{"command":"make","description":"Build"}`, "Exit code 0.\nok"),
		toolCall("mcp__dyna__shell", `{"command":"make","description":"Build"}`, "Still running after 30s, so it moved to the background as job "+job2+"."),
		toolCall("mcp__dyna__shell_output", `{"job_id":"`+job1+`","wait":"exit"}`, "Exit code 0.\n"),
		toolCall("mcp__dyna__shell_stop", `{"job_id":"`+job2+`"}`, "Exit code -1 (terminated)."),
	})
	LinkJobs(items, jobs)
	if got := items[0].Tool.JobID + "," + items[1].Tool.JobID; got != job1+","+job2 {
		t.Fatalf("runs = %q", got)
	}
}

func TestShellCallsAreNeverGroupedWithOtherCalls(t *testing.T) {
	patch := func(name, command, output string) workeroutput.Event {
		return toolCall(name, `{"command":"`+command+`","description":"Patch a file"}`, output)
	}
	items, _ := FromEvents([]workeroutput.Event{
		patch("mcp__dyna__shell", "apply_patch a.go", "Job "+job1+". Exit code 0."),
		patch("mcp__dyna__shell", "apply_patch b.go", "Job "+job2+". Exit code 0."),
		toolCall("Edit", `{"file_path":"/repo/c.go","old_string":"x"}`, ""),
		toolCall("Edit", `{"file_path":"/repo/d.go","old_string":"x"}`, ""),
	})
	if len(items) != 3 {
		t.Fatalf("items = %d, want 2 shell blocks and 1 grouped edit: %+v", len(items), items)
	}
	if items[0].Tool.JobID != job1 || items[1].Tool.JobID != job2 || items[2].Tool.Count != 2 {
		t.Fatalf("items = %+v", items)
	}
}

// Real Pi records from the PR #1165 live QA. The decoded end event's output
// must carry the exact job ID, so linking never depends on the command and
// description that identical concurrent calls share.
func TestSummarizeReadsPiShellJobIDsFromDecodedResults(t *testing.T) {
	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON}
	const qaJob = "sh_20261021-080938-d68f7c94"
	lines := []string{
		`{"type":"tool_execution_start","toolCallId":"call_ac6afd12dcd643a385e146db","toolName":"mcp__dyna__shell","args":{"command":"dyna --version","description":"Check dyna version"}}`,
		`{"type":"tool_execution_end","toolCallId":"call_ac6afd12dcd643a385e146db","toolName":"mcp__dyna__shell","result":{"content":[{"type":"text","text":"Job ` + qaJob + `. Exit code 0.\ndyna dev+df64d0752c9a\n"}],"details":{"server":"dyna","tool":"shell"},"structuredContent":{"content":[{"text":"Job ` + qaJob + `. Exit code 0.\ndyna dev+df64d0752c9a\n","type":"text"}],"isError":false}},"isError":false,"durationMs":212}`,
	}
	for _, line := range lines {
		for _, e := range workeroutput.DecodeLine(decl, line).Events {
			if e.Kind != "tool" {
				continue
			}
			tool := Summarize(e)
			if tool.JobOp != "run" {
				t.Fatalf("%s: tool op = %q", e.State, tool.JobOp)
			}
			switch e.State {
			case "started":
				if tool.JobID != "" || tool.Description != "Check dyna version" || tool.Detail != "dyna --version" {
					t.Errorf("start summarized %#v", tool)
				}
			case "completed":
				if tool.JobID != qaJob {
					t.Errorf("end linked to %q, want the exact job from the result", tool.JobID)
				}
			}
		}
	}
	// Two identical commands that run at once each keep their own job.
	for _, call := range []struct{ id, job string }{
		{"call_left", job1},
		{"call_right", job2},
	} {
		line := `{"type":"tool_execution_end","toolCallId":"` + call.id + `","toolName":"mcp__dyna__shell","result":{"content":[{"type":"text","text":"Job ` + call.job + `. Exit code 0.\n"}]}}`
		for _, e := range workeroutput.DecodeLine(decl, line).Events {
			if tool := Summarize(e); tool.JobID != call.job {
				t.Errorf("identical concurrent call %s linked to %q, want %q", call.id, tool.JobID, call.job)
			}
		}
	}
}
