package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

const countSchema = `{ schema: { type: 'object', required: ['count'], properties: { count: { type: 'number' } } } }`

// answers makes a harness whose workers answer with each text in turn, and records the prompt of every turn.
func answers(h *crewHarness, replies ...contract.TurnEnded) *[]string {
	var mu sync.Mutex
	var prompts []string
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		return replies[min(len(prompts), len(replies))-1], nil
	}
	return &prompts
}

func text(final string) contract.TurnEnded { return contract.TurnEnded{Final: final} }

func TestASchemaStepsFinalTextIsParsedAsItsResult(t *testing.T) {
	for name, final := range map[string]string{"bare": `{"count": 3}`, "padded": "\n {\"count\": 3}\n", "fenced": "```json\n{\"count\": 3}\n```"} {
		h := newCrewHarness(t)
		prompts := answers(h, text(final))
		got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
		if got["count"] != float64(3) || len(*prompts) != 1 || len(h.schemas) != 1 || !strings.Contains(h.schemas[0], `"count"`) {
			t.Errorf("%s: %v, %d turns, schemas %q", name, got, len(*prompts), h.schemas)
		}
	}
}

// A final text that is "Done.", prose around the JSON or JSON of the wrong shape goes back to the same worker with the error.
func TestAMismatchComesBackToTheSameWorkerAndARetrySucceeds(t *testing.T) {
	for name, first := range map[string]string{"done": "Done.", "prose": `Here you go: {"count": 3}`, "shape": `{"count": "many"}`, "two blocks": "```\n{\"count\": 1}\n```\n```\n{\"count\": 2}\n```"} {
		h := newCrewHarness(t)
		prompts := answers(h, text(first), text(`{"count": 4}`))
		got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
		if got["count"] != float64(4) || len(*prompts) != 2 || h.started.Load() != 1 {
			t.Fatalf("%s: %v, %d turns, %d workers", name, got, len(*prompts), h.started.Load())
		}
		if follow := (*prompts)[1]; !strings.Contains(follow, "not accepted") || !strings.Contains(follow, "only a JSON value") {
			t.Errorf("%s: the follow-up is %q", name, follow)
		}
	}
}

func TestThreeMismatchesFailTheStepWithTheLastText(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, text("Done."))
	crew, ended := h.start(crewMeta + `return 1`)
	<-ended
	out := crew.worker(t.Context(), strings.Repeat("0", 64), crewProfiles[0].Name, "p", agentOpts{Schema: []byte(`{"type":"object","required":["count"]}`)}, h.workdir)
	if out.cause != contract.CauseBadResult || out.value != nil || len(*prompts) != schemaAttempts || !strings.Contains(out.reason, "its last text: Done.") {
		t.Fatalf("outcome %+v after %d turns", out, len(*prompts))
	}
	if got := h.run(crewMeta + `return await agent('count them', ` + countSchema + `)`); got.Outcome != contract.TurnCompleted && !strings.Contains(got.Error, "schema") {
		t.Fatalf("turn %+v", got)
	}
}

// A reply that reached the output limit is told the result is too large for text, and to write it to a file.
func TestATooLargeReplyIsToldToWriteAFile(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, contract.TurnEnded{Outcome: contract.TurnFailed, Cause: contract.CauseOutputLimit}, text(`{"count": 2}`))
	got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
	if got["count"] != float64(2) || len(*prompts) != 2 || !strings.Contains((*prompts)[1], "write it to a file") && !strings.Contains((*prompts)[1], "Write it to a file") {
		t.Fatalf("%v, prompts %q", got, *prompts)
	}
}

func TestAStepWithoutASchemaKeepsItsFinalTextAsIs(t *testing.T) {
	h := newCrewHarness(t)
	prompts := answers(h, text("Done."))
	if got := h.result(crewMeta + `return await agent('plain')`); got != "Done." || len(*prompts) != 1 || fmt.Sprint(h.schemas) != "[]" && h.schemas[0] != "" {
		t.Fatalf("%v, prompts %q, schemas %q", got, *prompts, h.schemas)
	}
}

func TestReadResultAcceptsTheWholeTextOrOneFencedBlockOnly(t *testing.T) {
	schema, err := compileSchema([]byte(`{"type":"object","required":["count"]}`))
	if err != nil {
		t.Fatal(err)
	}
	const head, tail = `{"count":1,"pad":"`, `"}`
	for size, wantProblem := range map[int]bool{stepResultBytes: false, stepResultBytes + 1: true} {
		value, problem := readResult(schema, head+strings.Repeat("x", size-len(head)-len(tail))+tail)
		if (problem != "") != wantProblem || (value == nil) != wantProblem {
			t.Errorf("%d bytes: value %v, problem %q", size, value != nil, problem)
		}
	}
	for text, ok := range map[string]bool{"```\n{\"count\":1}\n```": true, "```json {\"count\":1}```": false, "ok ```{\"count\":1}```": false, "[1]": false, "": false} {
		if _, problem := readResult(schema, text); (problem == "") != ok {
			t.Errorf("%q: problem %q", text, problem)
		}
	}
}
